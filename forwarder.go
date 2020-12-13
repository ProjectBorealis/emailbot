package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mailgun/mailgun-go/v3"
	"github.com/sethvargo/go-password/password"
)

type EmailForwarder struct {
	mg         *mailgun.MailgunImpl
	ticker     *time.Ticker
	routes     []mailgun.Route
	routesLock sync.RWMutex
	prefix     string
}

func NewEmailForwarder(mg *mailgun.MailgunImpl, prefix string) (*EmailForwarder, error) {
	f := &EmailForwarder{mg: mg, prefix: prefix}
	f.ticker = time.NewTicker(10 * time.Minute)

	go func() {
		for range f.ticker.C {
			f.run()
		}
	}()

	return f, f.run()
}

func (f *EmailForwarder) run() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	iter := f.mg.ListRoutes(&mailgun.ListOptions{Limit: 1000})

	var page []mailgun.Route

	f.routesLock.Lock()
	defer f.routesLock.Unlock()

	fmt.Println("Updating routes...")

	f.routes = f.routes[:0]
	for iter.Next(ctx, &page) {
		for _, route := range page {
			f.routes = append(f.routes, route)
			fmt.Println(route)
		}
	}

	return nil
}

func (f *EmailForwarder) Close() error {
	f.ticker.Stop()
	return nil
}

func (f *EmailForwarder) routeByIdentifier(identifier string) (result *mailgun.Route) {
	f.routesLock.RLock()
	defer f.routesLock.RUnlock()

	for idx, route := range f.routes {
		if route.Description == f.prefix+identifier {
			result = &f.routes[idx]
			return
		}
	}
	return nil
}

func (f *EmailForwarder) routeByAlias(alias string) (result *mailgun.Route) {
	f.routesLock.RLock()
	defer f.routesLock.RUnlock()

	expr := fmt.Sprintf(`match_recipient("%s@%s")`, alias, f.mg.Domain())
	for idx, route := range f.routes {
		fmt.Printf("Expression: %s == %s\n", expr, route.Expression)
		if route.Expression == expr {
			result = &f.routes[idx]
			return
		}
	}
	return nil
}

func (f *EmailForwarder) Forward(alias string, destination string, identifier string) (string, string, error) {
	defer f.run()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	route := mailgun.Route{
		Priority:    1337,
		Description: f.prefix + identifier,
		Expression:  fmt.Sprintf(`match_recipient("%s@%s")`, alias, f.mg.Domain()),
		Actions:     []string{fmt.Sprintf(`forward("%s")`, destination)},
	}

	// error if alias already exists and is assigned to another identifier
	if current := f.routeByAlias(alias); current != nil && current.Description != f.prefix+identifier {
		return "", "", fmt.Errorf("`%s` already assigned to <@%s>", alias, strings.TrimPrefix(identifier, f.prefix))
	}

	// Add or update rule
	var err error
	if current := f.routeByIdentifier(identifier); current == nil {
		_, err = f.mg.CreateRoute(ctx, route)
	} else {
		_, err = f.mg.UpdateRoute(ctx, current.Id, route)
	}
	if err != nil {
		return "", "", err
	}

	user := "d-" + identifier
	pass, err := password.Generate(16, 5, 0, false, false)
	if err != nil {
		return "", "", err
	}

	return user + "@" + f.mg.Domain(), pass, f.mg.CreateCredential(ctx, user, pass)
}

func (f *EmailForwarder) Delete(identifier string) error {
	route := f.routeByIdentifier(identifier)
	if route == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	return f.mg.DeleteRoute(ctx, route.Id)
}
