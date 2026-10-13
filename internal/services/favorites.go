package services

import (
	"context"

	"github.com/hehehai/AudioSmith/internal/store"
)

// FavoritesScope is the favorites scope of the voice gallery's archetypes.
const FavoritesScope = "gallery"

// GalleryFavorites persists the gallery's favorites (gallery.FavoriteStore)
// in the store's favorites table, scope FavoritesScope.
type GalleryFavorites struct{ Store store.Favorites }

func (g GalleryFavorites) LoadFavorites(ctx context.Context) ([]string, error) {
	return g.Store.List(ctx, FavoritesScope)
}

func (g GalleryFavorites) SaveFavorite(ctx context.Context, id string, on bool) error {
	return g.Store.Set(ctx, FavoritesScope, id, on)
}
