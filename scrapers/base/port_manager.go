package base

import (
	"fmt"
	"sync"
)

// PortManager handles port allocation for goroutines
type PortManager struct {
	basePort  int
	portRange int
	PortMap   map[int]bool
	mutex     sync.Mutex
}

var (
	GlobalPortManager *PortManager
	once              sync.Once
)

// InitPortManager initializes the global port manager
func InitPortManager(basePort, portRange int) {
	once.Do(func() {
		GlobalPortManager = NewPortManager(basePort, portRange)
	})
}

// NewPortManager creates a new port manager with the specified base port and range
func NewPortManager(basePort, portRange int) *PortManager {
	PortMap := make(map[int]bool)
	for i := 0; i < portRange; i++ {
		PortMap[basePort+i] = false // Initialize all ports as available
	}

	return &PortManager{
		basePort:  basePort,
		portRange: portRange,
		PortMap:   PortMap,
		mutex:     sync.Mutex{},
	}
}

// GetPort allocates a port for the given goroutine ID
func (pm *PortManager) GetPort() (int, error) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	// Find an available port
	for i := 0; i < pm.portRange; i++ {
		port := pm.basePort + i
		if !pm.PortMap[port] {
			pm.PortMap[port] = true
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", pm.basePort, pm.basePort+pm.portRange-1)
}

// ReleasePort releases the port assigned to the given goroutine ID
func (pm *PortManager) ReleasePort(port int) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	pm.PortMap[port] = false
}
