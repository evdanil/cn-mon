package probe

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"sync"
)

func (p *Prober) ProbeBatch(ctx context.Context, targets []Resolved, ports []int) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("nil Prober")
	}
	if len(targets) > maxConcurrent {
		return nil, fmt.Errorf("ProbeBatch accepts at most %d targets", maxConcurrent)
	}
	for i, target := range targets {
		if !target.Address.IsValid() {
			return nil, fmt.Errorf("target %d has no resolved address", i)
		}
	}
	canonicalPorts, err := normalizePorts(ports)
	if err != nil {
		return nil, err
	}
	results := make([]Result, len(targets))
	for i, target := range targets {
		results[i] = Result{Target: target, TCP: make(map[int]TCPResult, len(canonicalPorts))}
	}
	if len(targets) == 0 {
		return results, nil
	}
	p.initLimits()
	var firstErr error
	var errMu sync.Mutex
	setErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := p.runICMP(ctx, targets, results); err != nil {
			setErr(err)
		}
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := p.runTCP(ctx, targets, canonicalPorts, results); err != nil {
			setErr(err)
		}
	}()
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func normalizePorts(ports []int) ([]int, error) {
	seen := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("TCP port %d is outside 1..65535", port)
		}
		seen[port] = struct{}{}
	}
	if len(seen) > 5 {
		return nil, errors.New("at most five distinct TCP ports are allowed")
	}
	result := make([]int, 0, len(seen))
	for port := range seen {
		result = append(result, port)
	}
	sort.Ints(result)
	return result, nil
}

func (p *Prober) runICMP(ctx context.Context, targets []Resolved, results []Result) error {
	if p.icmp == nil {
		return errors.New("ICMP adapter is not configured")
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	var errMu sync.Mutex
	var adapterErr error
	for i := 0; i < min(len(targets), maxConcurrent); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				select {
				case p.icmpLimit <- struct{}{}:
				case <-ctx.Done():
					continue
				}
				result, err := p.icmp(ctx, targets[index].Address)
				<-p.icmpLimit
				if err != nil {
					if ctx.Err() != nil {
						continue
					}
					errMu.Lock()
					if adapterErr == nil {
						adapterErr = err
					}
					errMu.Unlock()
					continue
				}
				results[index].ICMP = result
			}
		}()
	}
	for i := range targets {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil
		}
	}
	close(jobs)
	workers.Wait()
	return adapterErr
}

type tcpJob struct {
	index int
	addr  netip.Addr
	port  int
}

func (p *Prober) runTCP(ctx context.Context, targets []Resolved, ports []int, results []Result) error {
	count := len(targets) * len(ports)
	if count == 0 {
		return nil
	}
	jobs := make(chan tcpJob)
	completed := make([]TCPResult, count)
	workerCount := min(count, maxConcurrent)
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				select {
				case p.tcpLimit <- struct{}{}:
				case <-ctx.Done():
					continue
				}
				completed[job.index] = probeTCPWithDial(ctx, job.addr, job.port, p.dial)
				<-p.tcpLimit
			}
		}()
	}
	index := 0
send:
	for _, target := range targets {
		for _, port := range ports {
			select {
			case jobs <- tcpJob{index: index, addr: target.Address, port: port}:
				index++
			case <-ctx.Done():
				break send
			}
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil
	}
	for i := range targets {
		for j, port := range ports {
			results[i].TCP[port] = completed[i*len(ports)+j]
		}
	}
	return nil
}
