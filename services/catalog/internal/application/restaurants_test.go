package application

import (
	"context"
	"errors"
	"testing"

	"example.com/food/catalog/internal/domain"
)

type repositoryStub struct {
	called     bool
	ctx        context.Context
	err        error
	restaurant domain.Restaurant
	changes    domain.RestaurantChanges
	page       Page
}

func (r *repositoryStub) List(ctx context.Context, p Page) ([]domain.Restaurant, error) {
	r.called = true
	r.ctx = ctx
	r.page = p
	return []domain.Restaurant{r.restaurant}, r.err
}
func (r *repositoryStub) Get(ctx context.Context, id int64) (domain.Restaurant, error) {
	r.called = true
	r.ctx = ctx
	return r.restaurant, r.err
}
func (r *repositoryStub) Create(ctx context.Context, restaurant domain.Restaurant) (domain.Restaurant, error) {
	r.called = true
	r.ctx = ctx
	r.restaurant = restaurant
	return restaurant, r.err
}
func (r *repositoryStub) Update(ctx context.Context, id int64, c domain.RestaurantChanges) (domain.Restaurant, error) {
	r.called = true
	r.ctx = ctx
	r.changes = c
	return r.restaurant, r.err
}

func TestUseCasesValidateBeforePersistence(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Restaurants) error
		want error
	}{
		{"create", func(s *Restaurants) error { _, err := s.Create(context.Background(), " ", true); return err }, domain.ErrInvalidName},
		{"get", func(s *Restaurants) error { _, err := s.Get(context.Background(), 0); return err }, domain.ErrInvalidID},
		{"update id", func(s *Restaurants) error {
			_, err := s.Update(context.Background(), -1, domain.RestaurantChanges{})
			return err
		}, domain.ErrInvalidID},
		{"empty update", func(s *Restaurants) error {
			_, err := s.Update(context.Background(), 1, domain.RestaurantChanges{})
			return err
		}, domain.ErrEmptyUpdate},
		{"page limit", func(s *Restaurants) error { _, err := s.List(context.Background(), Page{Limit: 101}); return err }, ErrInvalidPagination},
		{"page offset", func(s *Restaurants) error {
			_, err := s.List(context.Background(), Page{Limit: 20, Offset: -1})
			return err
		}, ErrInvalidPagination},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &repositoryStub{}
			err := tt.run(NewRestaurants(repo))
			if !errors.Is(err, tt.want) || repo.called {
				t.Fatalf("error = %v, repository called = %v", err, repo.called)
			}
		})
	}
}

func TestUseCasesNormalizeAndPassContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &repositoryStub{}
	s := NewRestaurants(repo)
	r, err := s.Create(ctx, " Cafe ", true)
	if err != nil || r.Name != "Cafe" || !r.IsOpen || repo.ctx != ctx {
		t.Fatalf("create: %+v %v", r, err)
	}
	name := " Renamed "
	closed := false
	if _, err := s.Update(ctx, 1, domain.RestaurantChanges{Name: &name, IsOpen: &closed}); err != nil {
		t.Fatal(err)
	}
	if *repo.changes.Name != "Renamed" || *repo.changes.IsOpen || repo.ctx != ctx {
		t.Fatal("update did not preserve changes or context")
	}
	page := Page{Limit: 2, Offset: 3}
	if _, err := s.List(ctx, page); err != nil || repo.page != page || repo.ctx != ctx {
		t.Fatal("list did not preserve page or context")
	}
	if _, err := s.Get(ctx, 1); err != nil || repo.ctx != ctx {
		t.Fatal("get did not preserve context")
	}
}

func TestUseCasesPreserveRepositoryErrors(t *testing.T) {
	for _, want := range []error{domain.ErrNotFound, context.DeadlineExceeded, errors.New("database failure")} {
		s := NewRestaurants(&repositoryStub{err: want})
		_, getErr := s.Get(context.Background(), 1)
		_, createErr := s.Create(context.Background(), "Cafe", false)
		closed := false
		_, updateErr := s.Update(context.Background(), 1, domain.RestaurantChanges{IsOpen: &closed})
		_, listErr := s.List(context.Background(), Page{Limit: 20})
		for _, err := range []error{getErr, createErr, updateErr, listErr} {
			if !errors.Is(err, want) {
				t.Errorf("error %v lost cause %v", err, want)
			}
		}
	}
}
