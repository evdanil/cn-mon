package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
)

const maxConcurrent = 100

func (p *Prober) Resolve(ctx context.Context, hosts []string) ([]Resolved, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("nil Prober")
	}
	p.initLimits()
	results := make([]Resolved, len(hosts))
	unresolved := 0
	for i, host := range hosts {
		results[i] = Resolved{Index: i, Host: host}
		if addr, err := netip.ParseAddr(host); err == nil {
			results[i].Address = addr.Unmap()
		} else {
			unresolved++
		}
	}
	if unresolved == 0 {
		return results, nil
	}
	jobs := make(chan int)
	workers := min(unresolved, maxConcurrent)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil || results[index].Address.IsValid() {
					continue
				}
				result := p.resolveName(ctx, hosts[index])
				results[index].Address = result.addr
				results[index].LookupErr = result.err
			}
		}()
	}
	for i := range hosts {
		if results[i].Address.IsValid() {
			continue
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func lookupSystem(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

type lookupResult struct {
	addr netip.Addr
	err  error
}

func (p *Prober) resolveName(ctx context.Context, host string) lookupResult {
	lookup := p.lookup
	if lookup == nil {
		lookup = lookupSystem
	}
	var errs []error
	for _, network := range []string{"ip4", "ip6"} {
		select {
		case p.lookupLimit <- struct{}{}:
		case <-ctx.Done():
			return lookupResult{err: ctx.Err()}
		}
		answers, err := lookup(ctx, network, host)
		<-p.lookupLimit
		if err != nil {
			errs = append(errs, fmt.Errorf("%s lookup: %w", network, err))
			if ctx.Err() != nil {
				return lookupResult{err: ctx.Err()}
			}
			continue
		}
		for _, addr := range answers {
			addr = addr.Unmap()
			if network == "ip4" && addr.Is4() {
				return lookupResult{addr: addr}
			}
			if network == "ip6" && addr.Is6() {
				return lookupResult{addr: addr}
			}
		}
	}
	if len(errs) == 0 {
		return lookupResult{err: errors.New("DNS lookup returned no usable address records")}
	}
	return lookupResult{err: errors.Join(errs...)}
}
