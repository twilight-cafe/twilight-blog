package service

import (
	"context"
	"errors"

	"blog-engine/internal/model"
)

// 本文件提供一批「每个方法都返回同一个错误」的仓储替身。
//
// 为什么统一写成这种形状：服务层的错误分支几乎全部是「仓储返回错误 → 记日志 →
// 原样上抛」，它们没有各自的判断逻辑，所以替身只需要能**让每个方法失败一次**。
// 与其给每个仓储写一个精确替身（几十行 × 十几个仓储），不如共用一份：覆盖到的
// 是同一组分支，而断言强度由「错误必须原样冒泡」这一条守住。

var errRepoDown = errors.New("repository unavailable")

type brokenPageRepo struct{}

func (brokenPageRepo) Create(context.Context, *model.Page) error { return errRepoDown }
func (brokenPageRepo) Update(context.Context, *model.Page) error { return errRepoDown }
func (brokenPageRepo) SoftDelete(context.Context, []int64, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenPageRepo) Restore(context.Context, int64, int64) error { return errRepoDown }
func (brokenPageRepo) BatchRestore(context.Context, []int64, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenPageRepo) HardDelete(context.Context, []int64) (int64, error) { return 0, errRepoDown }
func (brokenPageRepo) GetByID(context.Context, int64) (*model.Page, error) {
	return nil, errRepoDown
}
func (brokenPageRepo) List(context.Context, model.Pagination) ([]model.Page, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenPageRepo) ListDeleted(context.Context, model.Pagination) ([]model.Page, int64, error) {
	return nil, 0, errRepoDown
}

type brokenMomentRepo struct{}

func (brokenMomentRepo) Create(context.Context, *model.Moment) error { return errRepoDown }
func (brokenMomentRepo) Update(context.Context, *model.Moment) error { return errRepoDown }
func (brokenMomentRepo) SoftDelete(context.Context, []int64, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenMomentRepo) Restore(context.Context, int64, int64) error { return errRepoDown }
func (brokenMomentRepo) BatchRestore(context.Context, []int64, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenMomentRepo) HardDelete(context.Context, []int64) (int64, error) { return 0, errRepoDown }
func (brokenMomentRepo) GetByID(context.Context, int64) (*model.Moment, error) {
	return nil, errRepoDown
}
func (brokenMomentRepo) List(context.Context, model.Pagination) ([]model.Moment, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenMomentRepo) ListDeleted(context.Context, model.Pagination) ([]model.Moment, int64, error) {
	return nil, 0, errRepoDown
}

type brokenTagRepo struct{}

func (brokenTagRepo) List(context.Context, model.Pagination) ([]model.Tag, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenTagRepo) GetByID(context.Context, int64) (*model.Tag, error) { return nil, errRepoDown }
func (brokenTagRepo) ListArticlesByTag(context.Context, int64, model.Pagination) ([]model.Article, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenTagRepo) CountByTag(context.Context, int64) (int64, error) { return 0, errRepoDown }
func (brokenTagRepo) Rename(context.Context, int64, string, string) error {
	return errRepoDown
}

type brokenCommentRepo struct{}

func (brokenCommentRepo) Create(context.Context, *model.Comment) error { return errRepoDown }
func (brokenCommentRepo) GetByID(context.Context, int64) (*model.Comment, error) {
	return nil, errRepoDown
}
func (brokenCommentRepo) GetByIDForUser(context.Context, int64, *int64) (*model.Comment, error) {
	return nil, errRepoDown
}
func (brokenCommentRepo) ListByTarget(context.Context, string, int64, *int64, model.Pagination) ([]model.Comment, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenCommentRepo) ListAll(context.Context, model.Pagination) ([]model.Comment, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenCommentRepo) ListDeleted(context.Context, model.Pagination) ([]model.Comment, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenCommentRepo) AddLike(context.Context, int64, int64, int64) error { return errRepoDown }
func (brokenCommentRepo) RemoveLike(context.Context, int64, int64) error     { return errRepoDown }
func (brokenCommentRepo) SoftDelete(context.Context, []int64, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenCommentRepo) Restore(context.Context, int64, int64) error { return errRepoDown }
func (brokenCommentRepo) BatchRestore(context.Context, []int64, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenCommentRepo) HardDelete(context.Context, []int64) (int64, error) { return 0, errRepoDown }

type brokenUserRepo struct{}

func (brokenUserRepo) FindByUsername(context.Context, string) (*model.User, error) {
	return nil, errRepoDown
}
func (brokenUserRepo) FindByEmail(context.Context, string) (*model.User, error) {
	return nil, errRepoDown
}
func (brokenUserRepo) GetByID(context.Context, int64) (*model.User, error) { return nil, errRepoDown }
func (brokenUserRepo) Create(context.Context, *model.User) error           { return errRepoDown }
func (brokenUserRepo) UpdateNickname(context.Context, int64, string, int64) error {
	return errRepoDown
}
func (brokenUserRepo) UpdateUsername(context.Context, int64, string, int64) error {
	return errRepoDown
}
func (brokenUserRepo) UpdatePassword(context.Context, int64, string, int64) error {
	return errRepoDown
}
func (brokenUserRepo) UpdateProfile(context.Context, int64, string, string, string, int64) error {
	return errRepoDown
}
func (brokenUserRepo) List(context.Context, model.Pagination) ([]model.User, int64, error) {
	return nil, 0, errRepoDown
}

type brokenAdminRepo struct{}

func (brokenAdminRepo) Create(context.Context, *model.User) error { return errRepoDown }
func (brokenAdminRepo) Get(context.Context) (*model.User, error)  { return nil, errRepoDown }
func (brokenAdminRepo) Exists(context.Context) (bool, error)      { return false, errRepoDown }
func (brokenAdminRepo) SetPassword(context.Context, int64, string, int64) error {
	return errRepoDown
}
func (brokenAdminRepo) UpdateIdentity(context.Context, int64, string, string, int64) error {
	return errRepoDown
}
func (brokenAdminRepo) UpdateAvatar(context.Context, int64, *int64, int64) error {
	return errRepoDown
}

type brokenArticleRepo struct{}

func (brokenArticleRepo) Create(context.Context, *model.Article) error { return errRepoDown }
func (brokenArticleRepo) Update(context.Context, *model.Article) error { return errRepoDown }
func (brokenArticleRepo) SoftDelete(context.Context, []int64, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenArticleRepo) Restore(context.Context, int64, int64) error { return errRepoDown }
func (brokenArticleRepo) BatchRestore(context.Context, []int64, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenArticleRepo) HardDelete(context.Context, []int64) (int64, error) { return 0, errRepoDown }
func (brokenArticleRepo) CoverMediaIDsToDelete(context.Context, []int64) ([]int64, error) {
	return nil, errRepoDown
}
func (brokenArticleRepo) GetByID(context.Context, int64) (*model.Article, error) {
	return nil, errRepoDown
}
func (brokenArticleRepo) List(context.Context, model.Pagination) ([]model.Article, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenArticleRepo) ListDeleted(context.Context, model.Pagination) ([]model.Article, int64, error) {
	return nil, 0, errRepoDown
}

type brokenEmailRepo struct{}

func (brokenEmailRepo) Create(context.Context, *model.Email) error { return errRepoDown }
func (brokenEmailRepo) CountSince(context.Context, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenEmailRepo) List(context.Context, model.Pagination) ([]model.Email, int64, error) {
	return nil, 0, errRepoDown
}
func (brokenEmailRepo) GetByID(context.Context, int64) (*model.Email, error) {
	return nil, errRepoDown
}

type brokenVerificationRepo struct{}

func (brokenVerificationRepo) Create(context.Context, *model.EmailVerificationCode) error {
	return errRepoDown
}
func (brokenVerificationRepo) FindLatestActiveByEmail(context.Context, string, int64) (*model.EmailVerificationCode, error) {
	return nil, errRepoDown
}
func (brokenVerificationRepo) ClaimCode(context.Context, int64, int64) error { return errRepoDown }
func (brokenVerificationRepo) InvalidateActiveForEmail(context.Context, string, int64) error {
	return errRepoDown
}
func (brokenVerificationRepo) CountCreatedSince(context.Context, string, int64) (int64, error) {
	return 0, errRepoDown
}
func (brokenVerificationRepo) LastCreatedAt(context.Context, string) (int64, bool, error) {
	return 0, false, errRepoDown
}
func (brokenVerificationRepo) ReserveAttempt(context.Context, int64, int) (bool, error) {
	return false, errRepoDown
}
