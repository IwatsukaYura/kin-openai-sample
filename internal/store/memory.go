// Package store はユーザーの永続化を担う。サンプルなのでインメモリ実装のみ。
package store

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/IwatsukaYura/kin-openai-sample/api"
)

var (
	ErrNotFound       = errors.New("user not found")
	ErrDuplicateEmail = errors.New("email already exists")
)

// ListFilter は一覧取得の絞り込み条件。
type ListFilter struct {
	Role  *api.Role
	Limit int
}

// Memory はスレッドセーフなインメモリのユーザーストア。
type Memory struct {
	mu     sync.RWMutex
	nextID int64
	users  map[int64]api.User
}

func NewMemory() *Memory {
	return &Memory{nextID: 1, users: map[int64]api.User{}}
}

func (m *Memory) Create(_ context.Context, in api.NewUser) (api.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, u := range m.users {
		if u.Email == in.Email {
			return api.User{}, ErrDuplicateEmail
		}
	}
	u := api.User{
		Id:    m.nextID,
		Name:  in.Name,
		Email: in.Email,
		Age:   in.Age,
		Role:  in.Role,
		Tags:  in.Tags,
	}
	m.users[u.Id] = u
	m.nextID++
	return u, nil
}

func (m *Memory) Get(_ context.Context, id int64) (api.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	u, ok := m.users[id]
	if !ok {
		return api.User{}, ErrNotFound
	}
	return u, nil
}

func (m *Memory) List(_ context.Context, f ListFilter) ([]api.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	users := make([]api.User, 0, len(m.users))
	for _, u := range m.users {
		if f.Role != nil && u.Role != *f.Role {
			continue
		}
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Id < users[j].Id })
	if f.Limit > 0 && len(users) > f.Limit {
		users = users[:f.Limit]
	}
	return users, nil
}
