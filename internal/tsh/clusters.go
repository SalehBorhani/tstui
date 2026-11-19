package tsh

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrClusterOffline means tsh reported the cluster offline, so its servers
// were not listed.
var ErrClusterOffline = errors.New("cluster offline")

// listConcurrency bounds the `tsh ls` runs of ListAll.
const listConcurrency = 4

// Listing is the servers of every cluster of one proxy.
type Listing struct {
	// Clusters is the root cluster then the leaf clusters; nil when tsh
	// could not list them and only the selected cluster was listed.
	Clusters []Cluster
	// Servers holds every cluster's servers, in Clusters order, each
	// with its Cluster set.
	Servers []Server
	// Errs maps a cluster name to the error of listing its servers. An
	// offline cluster has ErrClusterOffline.
	Errs map[string]error
}

// ListAll lists the clusters of proxy and then the servers of each, a few
// at a time, so one unreachable leaf cluster does not hide the others.
// When tsh cannot list the clusters it falls back to the selected cluster
// alone. The error is non-nil only when no cluster could be listed.
func ListAll(ctx context.Context, c Client, proxy string) (Listing, error) {
	clusters, err := c.ListClusters(ctx, proxy)
	if errors.Is(err, ErrNotLoggedIn) {
		return Listing{}, err
	}
	if err != nil || len(clusters) == 0 {
		servers, lerr := c.ListServers(ctx, proxy, "")
		if lerr != nil {
			return Listing{}, lerr
		}
		return Listing{Servers: servers}, nil
	}

	per := make([][]Server, len(clusters))
	errs := make([]error, len(clusters))
	sem := make(chan struct{}, listConcurrency)
	var wg sync.WaitGroup
	for i, cl := range clusters {
		if !cl.Online() {
			errs[i] = ErrClusterOffline
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			per[i], errs[i] = c.ListServers(ctx, proxy, cl.Name)
		}()
	}
	wg.Wait()

	out := Listing{Clusters: clusters, Errs: map[string]error{}}
	var firstErr error
	listed := 0
	for i, cl := range clusters {
		if errs[i] != nil {
			out.Errs[cl.Name] = errs[i]
			if firstErr == nil {
				firstErr = fmt.Errorf("cluster %s: %w", cl.Name, errs[i])
			}
			continue
		}
		listed++
		out.Servers = append(out.Servers, per[i]...)
	}
	if listed == 0 {
		return out, firstErr
	}
	return out, nil
}
