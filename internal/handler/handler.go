// Package handler は oapi-codegen が生成した api.ServerInterface の実装。
//
// 入力値の検証は validator ミドルウェア (kin-openapi) が済ませているので、
// ハンドラーはビジネスロジックだけに集中できる。
package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/IwatsukaYura/kin-openai-sample/api"
)

// Server はインメモリにユーザーを保持するだけのサンプル実装。
type Server struct {
	mu     sync.Mutex
	nextID int64
	users  map[int64]api.User
}

var _ api.ServerInterface = (*Server)(nil)

func New() *Server {
	return &Server{nextID: 1, users: map[int64]api.User{}}
}

func (s *Server) ListUsers(w http.ResponseWriter, _ *http.Request, params api.ListUsersParams) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := 20
	if params.Limit != nil {
		limit = *params.Limit
	}
	users := make([]api.User, 0, len(s.users))
	for _, u := range s.users {
		if params.Role != nil && u.Role != *params.Role {
			continue
		}
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Id < users[j].Id })
	if len(users) > limit {
		users = users[:limit]
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) CreateUser(w http.ResponseWriter, r *http.Request) {
	var body api.CreateUserJSONRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Error{Message: err.Error()})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	u := api.User{
		Id:    s.nextID,
		Name:  body.Name,
		Email: body.Email,
		Age:   body.Age,
		Role:  body.Role,
		Tags:  body.Tags,
	}
	s.users[u.Id] = u
	s.nextID++
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) GetUser(w http.ResponseWriter, _ *http.Request, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Error{Message: "user not found"})
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
