package application

import "context"

type workloadLock struct {
	ready chan struct{}
	users int
}

// lockWorkload serializes engine side effects in this Runtime instance.
// Durable operation keys, generation checks and repository CAS remain the
// authority across process restarts.
func (m *Manager) lockWorkload(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.operationsMu.Lock()
	if m.operations == nil {
		m.operations = make(map[string]*workloadLock)
	}
	entry := m.operations[id]
	if entry == nil {
		entry = &workloadLock{ready: make(chan struct{}, 1)}
		entry.ready <- struct{}{}
		m.operations[id] = entry
	}
	entry.users++
	m.operationsMu.Unlock()
	forget := func() {
		m.operationsMu.Lock()
		defer m.operationsMu.Unlock()
		entry.users--
		if entry.users == 0 {
			delete(m.operations, id)
		}
	}
	select {
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	case <-entry.ready:
		unlock := func() {
			entry.ready <- struct{}{}
			forget()
		}
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	}
}
