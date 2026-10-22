package model

import (
	"context"
	"testing"
)

// TestRepositoriesSurfaceClosedDatabaseErrors 把每个仓储方法在「连接不可用」
// 时的返回路径都跑一遍。生产代码里这些分支大多只负责包装错误；它们无法用真实
// 数据库构造，却正是一个数据库故障时用户看到的全部行为。这里用已关闭的
// *sql.DB 触发同一组分支，并只断言「不 panic、不挂住」。
func TestRepositoriesSurfaceClosedDatabaseErrors(t *testing.T) {
	db := openTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	ctx := context.Background()

	articleRepo := NewSQLiteArticleRepository(db)
	for _, call := range []func(){
		func() { _ = articleRepo.Create(ctx, &Article{}) },
		func() { _ = articleRepo.Update(ctx, &Article{}) },
		func() { _, _ = articleRepo.SoftDelete(ctx, []int64{1}, 1) },
		func() { _ = articleRepo.Restore(ctx, 1, 1) },
		func() { _, _ = articleRepo.BatchRestore(ctx, []int64{1}, 1) },
		func() { _, _ = articleRepo.HardDelete(ctx, []int64{1}) },
		func() { _, _ = articleRepo.CoverMediaIDsToDelete(ctx, []int64{1}) },
		func() { _, _ = articleRepo.GetByID(ctx, 1) },
		func() { _, _, _ = articleRepo.List(ctx, Pagination{}) },
		func() { _, _, _ = articleRepo.ListDeleted(ctx, Pagination{}) },
	} {
		call()
	}

	commentRepo := NewSQLiteCommentRepository(db)
	for _, call := range []func(){
		func() { _ = commentRepo.Create(ctx, &Comment{}) },
		func() { _, _ = commentRepo.GetByID(ctx, 1) },
		func() { _, _ = commentRepo.GetByIDForUser(ctx, 1, nil) },
		func() { _, _, _ = commentRepo.ListByTarget(ctx, "article", 1, nil, Pagination{}) },
		func() { _, _, _ = commentRepo.ListAll(ctx, Pagination{}) },
		func() { _, _, _ = commentRepo.ListDeleted(ctx, Pagination{}) },
		func() { _ = commentRepo.AddLike(ctx, 1, 1, 1) },
		func() { _ = commentRepo.RemoveLike(ctx, 1, 1) },
		func() { _, _ = commentRepo.SoftDelete(ctx, []int64{1}, 1) },
		func() { _ = commentRepo.Restore(ctx, 1, 1) },
		func() { _, _ = commentRepo.BatchRestore(ctx, []int64{1}, 1) },
		func() { _, _ = commentRepo.HardDelete(ctx, []int64{1}) },
	} {
		call()
	}

	momentRepo := NewSQLiteMomentRepository(db)
	for _, call := range []func(){
		func() { _ = momentRepo.Create(ctx, &Moment{}) },
		func() { _ = momentRepo.Update(ctx, &Moment{}) },
		func() { _, _ = momentRepo.SoftDelete(ctx, []int64{1}, 1) },
		func() { _ = momentRepo.Restore(ctx, 1, 1) },
		func() { _, _ = momentRepo.BatchRestore(ctx, []int64{1}, 1) },
		func() { _, _ = momentRepo.HardDelete(ctx, []int64{1}) },
		func() { _, _ = momentRepo.GetByID(ctx, 1) },
		func() { _, _, _ = momentRepo.List(ctx, Pagination{}) },
		func() { _, _, _ = momentRepo.ListDeleted(ctx, Pagination{}) },
	} {
		call()
	}

	pageRepo := NewSQLitePageRepository(db)
	for _, call := range []func(){
		func() { _ = pageRepo.Create(ctx, &Page{}) },
		func() { _ = pageRepo.Update(ctx, &Page{}) },
		func() { _, _ = pageRepo.SoftDelete(ctx, []int64{1}, 1) },
		func() { _ = pageRepo.Restore(ctx, 1, 1) },
		func() { _, _ = pageRepo.BatchRestore(ctx, []int64{1}, 1) },
		func() { _, _ = pageRepo.HardDelete(ctx, []int64{1}) },
		func() { _, _ = pageRepo.GetByID(ctx, 1) },
		func() { _, _, _ = pageRepo.List(ctx, Pagination{}) },
		func() { _, _, _ = pageRepo.ListDeleted(ctx, Pagination{}) },
	} {
		call()
	}

	mediaRepo := NewSQLiteMediaRepository(db)
	for _, call := range []func(){
		func() { _ = mediaRepo.Create(ctx, &Media{}) },
		func() { _, _, _ = mediaRepo.List(ctx, Pagination{}) },
		func() { _ = mediaRepo.Delete(ctx, 1) },
		func() { _ = mediaRepo.UpdateStorage(ctx, 1, "x", "image/png", 1, false) },
		func() { _, _ = mediaRepo.ListImagesForWebPUpgrade(ctx) },
	} {
		call()
	}

	emailRepo := NewSQLiteEmailRepository(db)
	for _, call := range []func(){
		func() { _ = emailRepo.Create(ctx, &Email{}) },
		func() { _, _ = emailRepo.CountSince(ctx, 1) },
		func() { _, _, _ = emailRepo.List(ctx, Pagination{}) },
		func() { _, _ = emailRepo.GetByID(ctx, 1) },
	} {
		call()
	}

	codeRepo := NewSQLiteEmailVerificationRepository(db)
	for _, call := range []func(){
		func() { _ = codeRepo.Create(ctx, &EmailVerificationCode{}) },
		func() { _, _ = codeRepo.FindLatestActiveByEmail(ctx, "a@b.co", 1) },
		func() { _ = codeRepo.ClaimCode(ctx, 1, 1) },
		func() { _ = codeRepo.InvalidateActiveForEmail(ctx, "a@b.co", 1) },
		func() { _, _ = codeRepo.CountCreatedSince(ctx, "a@b.co", 1) },
		func() { _, _, _ = codeRepo.LastCreatedAt(ctx, "a@b.co") },
		func() { _, _ = codeRepo.ReserveAttempt(ctx, 1, 1) },
	} {
		call()
	}

	userRepo := NewSQLiteUserRepository(db)
	for _, call := range []func(){
		func() { _, _ = userRepo.FindByUsername(ctx, "x") },
		func() { _, _ = userRepo.FindByEmail(ctx, "x@y.co") },
		func() { _, _ = userRepo.GetByID(ctx, 1) },
		func() { _ = userRepo.Create(ctx, &User{}) },
		func() { _ = userRepo.UpdateNickname(ctx, 1, "x", 1) },
		func() { _ = userRepo.UpdateUsername(ctx, 1, "x", 1) },
		func() { _ = userRepo.UpdatePassword(ctx, 1, "h", 1) },
		func() { _ = userRepo.UpdateProfile(ctx, 1, "x", "x", "x@y.co", 1) },
		func() { _, _, _ = userRepo.List(ctx, Pagination{}) },
	} {
		call()
	}

	adminRepo := NewSQLiteAdminRepository(db)
	for _, call := range []func(){
		func() { _ = adminRepo.Create(ctx, &User{}) },
		func() { _, _ = adminRepo.Get(ctx) },
		func() { _, _ = adminRepo.Exists(ctx) },
		func() { _ = adminRepo.SetPassword(ctx, 1, "h", 1) },
		func() { _ = adminRepo.UpdateIdentity(ctx, 1, "x", "x", 1) },
		func() { _ = adminRepo.UpdateAvatar(ctx, 1, nil, 1) },
	} {
		call()
	}

	tagRepo := NewSQLiteTagRepository(db)
	for _, call := range []func(){
		func() { _, _, _ = tagRepo.List(ctx, Pagination{}) },
		func() { _, _ = tagRepo.GetByID(ctx, 1) },
		func() { _, _, _ = tagRepo.ListArticlesByTag(ctx, 1, Pagination{}) },
		func() { _, _ = tagRepo.CountByTag(ctx, 1) },
		func() { _ = tagRepo.Rename(ctx, 1, "x", "") },
	} {
		call()
	}
}
