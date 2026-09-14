// Package forward contains stream forwarding utilities.
package forward

import (
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
)

// ErrDestNotFound is returned when a forward destination is not found.
var ErrDestNotFound = errors.New("forward destination not found")

// ManagerParent is the parent interface.
type ManagerParent interface {
	logger.Writer
}

// Manager manages the forward destinations of a path.
type Manager struct {
	ReadTimeout       conf.Duration
	WriteTimeout      conf.Duration
	UDPMaxPayloadSize int
	PathName          string
	Matches           []string
	Forward           conf.Forward
	Parent            ManagerParent

	mutex        sync.RWMutex
	destHandlers []*DestHandler
	started      bool
	stream       *stream.Stream
}

// Initialize initializes Manager.
func (m *Manager) Initialize() {
	m.destHandlers = make([]*DestHandler, 0, len(m.Forward))

	for i, dest := range m.Forward {
		destHandler := m.createDestHandler(i+1, dest)
		m.destHandlers = append(m.destHandlers, destHandler)
	}
}

// Log implements logger.Writer.
func (m *Manager) Log(level logger.Level, format string, args ...any) {
	m.Parent.Log(level, format, args...)
}

func (m *Manager) createDestHandler(pos int, conf conf.ForwardDest) *DestHandler {
	handler := &DestHandler{
		Pos:               pos,
		Conf:              conf,
		ReadTimeout:       m.ReadTimeout,
		WriteTimeout:      m.WriteTimeout,
		UDPMaxPayloadSize: m.UDPMaxPayloadSize,
		PathName:          m.PathName,
		Matches:           m.Matches,
		Parent:            m,
	}
	handler.initialize()
	return handler
}

// ReloadConf reloads statically-configured destinations.
func (m *Manager) ReloadConf(forward conf.Forward) {
	m.mutex.Lock()

	newHandlers := make([]*DestHandler, len(forward))
	toClose := make([]*DestHandler, 0)

	for i, dest := range forward {
		if i < len(m.destHandlers) && m.destHandlers[i].Conf == dest {
			newHandlers[i] = m.destHandlers[i]
		} else {
			if i < len(m.destHandlers) {
				toClose = append(toClose, m.destHandlers[i])
			}

			destHandler := m.createDestHandler(i+1, dest)
			if m.started {
				destHandler.start(m.stream)
			}

			newHandlers[i] = destHandler
		}
	}

	for i := len(forward); i < len(m.destHandlers); i++ {
		toClose = append(toClose, m.destHandlers[i])
	}

	m.destHandlers = newHandlers

	m.mutex.Unlock()

	if m.started {
		for _, handler := range toClose {
			handler.stop()
		}
	}
}

// Start starts all forward destinations.
func (m *Manager) Start(strm *stream.Stream) {
	m.mutex.Lock()
	m.started = true
	m.stream = strm
	handlers := append([]*DestHandler(nil), m.destHandlers...)
	m.mutex.Unlock()

	for _, dest := range handlers {
		dest.start(strm)
	}
}

// Stop stops all forward destinations.
func (m *Manager) Stop() {
	m.mutex.Lock()
	m.started = false
	handlers := append([]*DestHandler(nil), m.destHandlers...)
	m.mutex.Unlock()

	for _, dest := range handlers {
		dest.stop()
	}
}

// APIAdd adds a forward destination on demand and starts it if the manager is running.
func (m *Manager) APIAdd(dest conf.ForwardDest) (*defs.APIForwardDest, error) {
	if err := dest.Validate(); err != nil {
		return nil, err
	}

	m.mutex.Lock()

	handler := m.createDestHandler(len(m.destHandlers)+1, dest)
	if m.started {
		handler.start(m.stream)
	}
	m.destHandlers = append(m.destHandlers, handler)

	m.mutex.Unlock()

	item := handler.APIItem()
	return &item, nil
}

// APIDelete stops and removes a forward destination that was added on demand or is statically configured.
func (m *Manager) APIDelete(id uuid.UUID) error {
	m.mutex.Lock()

	index := -1
	for i, handler := range m.destHandlers {
		if handler.ID() == id {
			index = i
			break
		}
	}

	if index < 0 {
		m.mutex.Unlock()
		return ErrDestNotFound
	}

	handler := m.destHandlers[index]
	m.destHandlers = append(m.destHandlers[:index:index], m.destHandlers[index+1:]...)
	started := m.started

	m.mutex.Unlock()

	if started {
		handler.stop()
	}

	return nil
}

// APIGet gets a forward destination.
func (m *Manager) APIGet(id uuid.UUID) (*defs.APIForwardDest, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	for _, handler := range m.destHandlers {
		if handler.ID() == id {
			item := handler.APIItem()
			return &item, nil
		}
	}

	return nil, ErrDestNotFound
}

// APIList lists all forward destinations.
func (m *Manager) APIList() *defs.APIForwardDestList {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	items := make([]defs.APIForwardDest, len(m.destHandlers))
	for i, handler := range m.destHandlers {
		items[i] = handler.APIItem()
	}

	return &defs.APIForwardDestList{Items: items}
}
