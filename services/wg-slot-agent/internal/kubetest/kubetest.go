package kubetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	"nacfson_pipeline/services/wg-slot-agent/internal/kube"
)

type FakeAPI struct {
	mu         sync.Mutex
	Server     *httptest.Server
	Leases     map[string]kube.Lease
	Nodes      map[string]bool
	Secrets    map[string]kube.Secret
	ConfigMaps map[string]kube.ConfigMap
	Events     []kube.Event
	BeforePut  func(name string)
	FailPuts   bool
	rv         int
}

func NewFakeAPI() *FakeAPI {
	f := &FakeAPI{Leases: map[string]kube.Lease{}, Nodes: map[string]bool{}, Secrets: map[string]kube.Secret{}, ConfigMaps: map[string]kube.ConfigMap{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *FakeAPI) Client() *kube.Client {
	return &kube.Client{BaseURL: f.Server.URL, Namespace: "oci-link", HTTP: f.Server.Client()}
}

func (f *FakeAPI) Close() { f.Server.Close() }

func (f *FakeAPI) nextRV() string {
	f.rv++
	return strconv.Itoa(f.rv)
}

func (f *FakeAPI) PutLease(l kube.Lease) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l.Metadata.ResourceVersion = f.nextRV()
	f.Leases[l.Metadata.Name] = l
}

func (f *FakeAPI) Lease(name string) kube.Lease {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Leases[name]
}

func (f *FakeAPI) EventReasons() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.Events {
		out = append(out, e.Reason)
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *FakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[2] == "nodes" {
		f.mu.Lock()
		ok := f.Nodes[parts[3]]
		f.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"reason": "NotFound"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"metadata": map[string]string{"name": parts[3]}})
		return
	}
	i := 0
	for i < len(parts) && parts[i] != "namespaces" {
		i++
	}
	if i+2 >= len(parts) {
		http.NotFound(w, r)
		return
	}
	resource := parts[i+2]
	name := ""
	if i+3 < len(parts) {
		name = parts[i+3]
	}
	switch resource {
	case "leases":
		f.leases(w, r, name)
	case "secrets":
		f.secrets(w, r, name)
	case "configmaps":
		f.configMaps(w, r, name)
	case "events":
		var e kube.Event
		_ = json.NewDecoder(r.Body).Decode(&e)
		if e.InvolvedObject.Namespace != parts[i+1] {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "involvedObject.namespace: does not match event.namespace"})
			return
		}
		f.mu.Lock()
		f.Events = append(f.Events, e)
		f.mu.Unlock()
		writeJSON(w, http.StatusCreated, e)
	default:
		http.NotFound(w, r)
	}
}

func (f *FakeAPI) leases(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method == http.MethodPut && f.BeforePut != nil {
		f.BeforePut(name)
	}
	if r.Method == http.MethodPut && f.FailPuts {
		writeJSON(w, http.StatusServiceUnavailable, nil)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && name == "":
		items := []kube.Lease{}
		for _, l := range f.Leases {
			items = append(items, l)
		}
		writeJSON(w, http.StatusOK, kube.LeaseList{Items: items})
	case r.Method == http.MethodGet:
		l, ok := f.Leases[name]
		if !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		writeJSON(w, http.StatusOK, l)
	case r.Method == http.MethodPost:
		var l kube.Lease
		_ = json.NewDecoder(r.Body).Decode(&l)
		if _, ok := f.Leases[l.Metadata.Name]; ok {
			writeJSON(w, http.StatusConflict, nil)
			return
		}
		l.Metadata.ResourceVersion = f.nextRV()
		f.Leases[l.Metadata.Name] = l
		writeJSON(w, http.StatusCreated, l)
	case r.Method == http.MethodPut:
		var l kube.Lease
		_ = json.NewDecoder(r.Body).Decode(&l)
		cur, ok := f.Leases[name]
		if !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		if cur.Metadata.ResourceVersion != l.Metadata.ResourceVersion {
			writeJSON(w, http.StatusConflict, nil)
			return
		}
		l.Metadata.ResourceVersion = f.nextRV()
		f.Leases[name] = l
		writeJSON(w, http.StatusOK, l)
	}
}

func (f *FakeAPI) secrets(w http.ResponseWriter, r *http.Request, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		s, ok := f.Secrets[name]
		if !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		writeJSON(w, http.StatusOK, s)
	case http.MethodPost, http.MethodPut:
		var s kube.Secret
		_ = json.NewDecoder(r.Body).Decode(&s)
		cur, exists := f.Secrets[s.Metadata.Name]
		if (r.Method == http.MethodPost && exists) || (r.Method == http.MethodPut && cur.Metadata.ResourceVersion != s.Metadata.ResourceVersion) {
			writeJSON(w, http.StatusConflict, nil)
			return
		}
		s.Metadata.ResourceVersion = f.nextRV()
		f.Secrets[s.Metadata.Name] = s
		writeJSON(w, http.StatusOK, s)
	}
}

func (f *FakeAPI) configMaps(w http.ResponseWriter, r *http.Request, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		cm, ok := f.ConfigMaps[name]
		if !ok {
			writeJSON(w, http.StatusNotFound, nil)
			return
		}
		writeJSON(w, http.StatusOK, cm)
	case http.MethodPost, http.MethodPut:
		var cm kube.ConfigMap
		_ = json.NewDecoder(r.Body).Decode(&cm)
		cur, exists := f.ConfigMaps[cm.Metadata.Name]
		if (r.Method == http.MethodPost && exists) || (r.Method == http.MethodPut && cur.Metadata.ResourceVersion != cm.Metadata.ResourceVersion) {
			writeJSON(w, http.StatusConflict, nil)
			return
		}
		cm.Metadata.ResourceVersion = f.nextRV()
		f.ConfigMaps[cm.Metadata.Name] = cm
		writeJSON(w, http.StatusOK, cm)
	}
}
