// Package handler は oapi-codegen が生成した api.StrictServerInterface の実装。
//
// OpenAPI 定義で表現できる入力チェック (型・必須・範囲・enum・format など) は
// kin-openapi のミドルウェアで済んでいるので、ここでは業務ルールだけを扱う。
package handler

import (
	"context"
	"errors"

	"github.com/IwatsukaYura/kin-openai-sample/api"
	"github.com/IwatsukaYura/kin-openai-sample/internal/store"
)

const defaultLimit = 20

// UserStore はハンドラーが依存する永続化層のインターフェース。
type UserStore interface {
	Create(ctx context.Context, in api.NewUser) (api.User, error)
	Get(ctx context.Context, id int64) (api.User, error)
	List(ctx context.Context, f store.ListFilter) ([]api.User, error)
}

type Handler struct {
	users UserStore
}

var _ api.StrictServerInterface = (*Handler)(nil)

func New(users UserStore) *Handler {
	return &Handler{users: users}
}

func (h *Handler) ListUsers(ctx context.Context, req api.ListUsersRequestObject) (api.ListUsersResponseObject, error) {
	limit := defaultLimit
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	users, err := h.users.List(ctx, store.ListFilter{Role: req.Params.Role, Limit: limit})
	if err != nil {
		return nil, err
	}
	return api.ListUsers200JSONResponse(users), nil
}

func (h *Handler) CreateUser(ctx context.Context, req api.CreateUserRequestObject) (api.CreateUserResponseObject, error) {
	u, err := h.users.Create(ctx, *req.Body)
	switch {
	case errors.Is(err, store.ErrDuplicateEmail):
		// 「メールアドレスの重複」のような状態に依存するルールは OpenAPI では表現できないので、ここで扱う
		return api.CreateUser409JSONResponse{Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.CreateUser201JSONResponse(u), nil
}

func (h *Handler) GetUser(ctx context.Context, req api.GetUserRequestObject) (api.GetUserResponseObject, error) {
	u, err := h.users.Get(ctx, req.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return api.GetUser404JSONResponse{Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return api.GetUser200JSONResponse(u), nil
}
