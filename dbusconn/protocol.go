package dbusconn

import (
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"github.com/op/go-logging"
)

const (
	propertyLogLevel    = "LogLevel"
	signalBridgeAdded   = "BridgeAdded"
	signalBridgeRemoved = "BridgeRemoved"
)

// OperabilityState informs if the device work
type BridgeState string

// ProtocolInterface callback called from Protocol Dbus Methods
type ProtocolInterface interface {
	AddDevice(*Device)
	RemoveDevice(string)
	AddItem(*Item)
	RemoveItem(string, string)
}

// Protocol is a dbus object which represents the states of a protocol
type Protocol struct {
	Callbacks ProtocolInterface
	dc        *Dbus
	Devices   map[string]*Device
	ready     bool
	log       *logging.Logger
	sync.Mutex
}

// RootProtocol is a dbus object which represents the states of the root protocol
type RootProto struct {
	Protocol   *Protocol
	dc         *Dbus
	properties *prop.Properties
	log        *logging.Logger
}

// Protocol is a dbus object which represents the states of a bridge protocol
type BridgeProto struct {
	Protocol *Protocol
	dc       *Dbus
}

func (dc *Dbus) exportRootProtocolObject(protocol string) (*Protocol, bool) {
	if dc.conn == nil {
		dc.Log.Warning("Unable to export Protocol dbus object because dbus connection nil")
		return nil, false
	}

	var proto = &Protocol{ready: false, dc: dc, Devices: make(map[string]*Device), log: dc.Log}
	path := dbus.ObjectPath(dbusPathPrefix + protocol)

	// properties
	propsSpec := initProtocolProp(&dc.RootProtocol)
	properties, err := prop.Export(dc.conn, path, propsSpec)
	if err == nil {
		dc.RootProtocol.properties = properties
	} else {
		proto.log.Error("Fail to export the properties of the protocol", proto, err)
	}

	err = dc.conn.Export(proto, path, dbusProtocolInterface)
	if err != nil {
		proto.log.Warning("Fail to export Module dbus object", err)
		return nil, false
	}
	err = dc.conn.Export(dc.RootProtocol, path, dbusProtocolInterface)
	if err != nil {
		proto.log.Warning("Fail to export Module dbus object", err)
		return nil, false
	}

	return proto, true
}

func (p *Protocol) setReady() {
	p.Lock()
	p.ready = true
	p.Unlock()
}

// IsReady dbus method to know if the protocol is ready or not
func (p *Protocol) IsReady() (bool, *dbus.Error) {
	p.Lock()
	var ready = p.ready
	p.Unlock()

	return ready, nil
}

func (dc *Dbus) emitBridgeAdded(bridgeID string) {
	path := dbus.ObjectPath(dbusPathPrefix + dc.ProtocolName + "_" + bridgeID)
	dc.conn.Emit(path, dbusProtocolInterface+"."+signalBridgeAdded)
}

func (dc *Dbus) emitBridgeRemoved(bridgeID string) {
	path := dbus.ObjectPath(dbusPathPrefix + dc.ProtocolName + "_" + bridgeID)
	dc.conn.Emit(path, dbusProtocolInterface+"."+signalBridgeRemoved)
}

//AddDevice is the dbus method to add a new device
func (p *Protocol) AddDevice(devID string, comID string, typeID string, typeVersion string, options []byte) (bool, *dbus.Error) {
	p.Lock()
	_, alreadyAdded := p.Devices[devID]
	if !alreadyAdded {
		device := initDevice(devID, comID, typeID, typeVersion, options, p)
		p.Devices[devID] = device
		p.dc.exportDeviceOnDbus(p.Devices[devID])
		if !isNil(p.Callbacks) {
			go p.Callbacks.AddDevice(p.Devices[devID])
		}
		p.dc.emitDeviceAdded(device)
	}
	p.Unlock()
	return alreadyAdded, nil
}

//RemoveDevice is the dbus method to remove a device
func (p *Protocol) RemoveDevice(devID string) *dbus.Error {
	p.Lock()
	device, devicePresent := p.Devices[devID]

	if !devicePresent {
		p.Unlock()
		return nil
	}
	device.Lock()

	for item := range device.Items {
		delete(device.Items, item)
	}
	if !isNil(p.Callbacks) {
		go p.Callbacks.RemoveDevice(devID)
	}
	device.Unlock()
	delete(p.Devices, devID)
	p.dc.emitDeviceRemoved(devID)
	p.Unlock()
	return nil
}

//AddBridge is the dbus method to add a new bridge
func (r *RootProto) AddBridge(bridgeID string) (bool, *dbus.Error) {
	r.Protocol.Lock()
	_, alreadyAdded := r.dc.Bridges[bridgeID]
	if !alreadyAdded {
		var proto = &Protocol{ready: false, dc: r.dc, Devices: make(map[string]*Device), log: r.dc.Log}
		path := dbus.ObjectPath(dbusPathPrefix + r.dc.ProtocolName + "_" + bridgeID)

		err := r.dc.conn.Export(proto, path, dbusProtocolInterface)
		if err != nil {
			proto.log.Warning("Fail to export Module dbus object", err)
		}
		var bridge = &BridgeProto{Protocol: proto, dc: r.dc}
		r.dc.Bridges[bridgeID] = bridge
		r.dc.emitBridgeAdded(bridgeID)
	}
	r.Protocol.Unlock()
	return alreadyAdded, nil
}

//RemoveBridge is the dbus method to remove a bridge
func (r *RootProto) RemoveBridge(bridgeID string) *dbus.Error {

	r.Protocol.Lock()
	bridge, bridgePresent := r.dc.Bridges[bridgeID]

	if !bridgePresent {
		r.Protocol.Unlock()
		return nil
	}
	bridge.Protocol.Lock()

	for device := range bridge.Protocol.Devices {
		bridge.Protocol.RemoveDevice(device)
	}
	bridge.Protocol.Unlock()
	delete(r.dc.Bridges, bridgeID)
	r.dc.emitBridgeRemoved(bridgeID)
	r.Protocol.Unlock()
	return nil
}

func (r *RootProto) setLogLevel(c *prop.Change) *dbus.Error {
	loglevel := c.Value.(string)
	level, err := logging.LogLevel(loglevel)
	if err == nil {
		logging.SetLevel(level, r.dc.Log.Module)
		r.log.Info("Log level has been set to ", c.Value.(string))
		return &dbus.ErrMsgInvalidArg
	} else {
		r.log.Error(err)
	}
	return nil
}

func initProtocolProp(r *RootProto) map[string]map[string]*prop.Prop {
	return map[string]map[string]*prop.Prop{
		dbusProtocolInterface: {
			propertyLogLevel: {
				Value:    logging.GetLevel(r.log.Module).String(),
				Writable: true,
				Emit:     prop.EmitTrue,
				Callback: r.setLogLevel,
			},
		},
	}
}
