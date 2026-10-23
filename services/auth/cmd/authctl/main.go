// authctl is an offline deployment-admin tool. It is not a public signup API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/auth/internal/adapters/postgres"
	"github.com/Desu-php/food/services/auth/internal/adapters/tokens"
	"github.com/Desu-php/food/services/auth/internal/application"
	"github.com/Desu-php/food/services/auth/internal/domain"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "authctl failed: check input, identity conflicts, permissions and database; no credentials were logged")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("command required")
	}
	f := flag.NewFlagSet("authctl", flag.ContinueOnError)
	operator := f.String("operator", "", "audited deployment operator")
	registry := f.String("registry", "", "legacy registry file")
	out := f.String("out", "", "new private key ring path")
	kid := f.String("kid", "", "new signing key ID")
	userID := f.Int64("user-id", 0, "managed user to disable")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if os.Args[1] == "keygen" {
		if *out == "" || *kid == "" {
			return errors.New("out and kid required")
		}
		key, err := tokens.GenerateKey(*kid)
		if err != nil {
			return err
		}
		body, err := json.MarshalIndent(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{key}}, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.Write(body)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	passwords, err := tokens.NewPasswords(19456, 2, 1, 2)
	if err != nil {
		return err
	}
	admin := application.NewAdministration(postgres.New(pool), passwords)
	switch os.Args[1] {
	case "disable":
		return admin.Disable(ctx, *userID, *operator)
	case "import-registry":
		file, err := os.Open(*registry)
		if err != nil {
			return err
		}
		defer file.Close()
		var source struct {
			Issuer     string               `json:"issuer"`
			Identities []security.Principal `json:"identities"`
		}
		d := json.NewDecoder(io.LimitReader(file, 1<<20))
		d.DisallowUnknownFields()
		if d.Decode(&source) != nil || d.Decode(new(any)) != io.EOF {
			return domain.ErrInvalid
		}
		users := make([]domain.User, 0, len(source.Identities))
		for _, p := range source.Identities {
			users = append(users, domain.User{ID: p.UserID, Subject: p.Subject, CustomerID: p.CustomerID, CourierID: p.CourierID, Roles: p.Roles, Permissions: p.Permissions, Restaurants: p.Restaurants})
		}
		return admin.Import(ctx, source.Issuer, users, *operator)
	case "provision":
		var in struct {
			UserID      int64    `json:"user_id"`
			Email       string   `json:"email"`
			Password    string   `json:"password"`
			Roles       []string `json:"roles"`
			Permissions []string `json:"permissions"`
			Restaurants []int64  `json:"restaurant_ids"`
			CustomerID  int64    `json:"customer_id"`
			CourierID   int64    `json:"courier_id"`
		}
		d := json.NewDecoder(io.LimitReader(os.Stdin, 16<<10))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
			return domain.ErrInvalid
		}
		return admin.Provision(ctx, domain.User{ID: in.UserID, Email: in.Email, Roles: in.Roles, Permissions: in.Permissions, Restaurants: in.Restaurants, CustomerID: in.CustomerID, CourierID: in.CourierID}, in.Password, *operator)
	default:
		return errors.New("unknown command")
	}
}
