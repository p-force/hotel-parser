// Package schedule содержит platform-independent planner и application service.
package schedule

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/p-force/hotel-parser/internal/config"
)

type Registry struct {
	Groups []Group `json:"groups"`
}
type Group struct {
	ID         string     `json:"id"`
	Schema     string     `json:"schema"`
	Properties []Property `json:"properties"`
}
type Property struct {
	Config string `json:"config"`
	// LOS — устаревший явный single-LOS override; 0 использует collection.nights.
	LOS          int           `json:"los,omitempty"`
	LOS2Evidence string        `json:"los2_evidence,omitempty"`
	Hotel        config.Config `json:"-"`
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func LoadRegistry(path string) (Registry, error) {
	f, err := os.Open(path)
	if err != nil {
		return Registry{}, fmt.Errorf("cannot open group registry")
	}
	defer f.Close()
	var r Registry
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, fmt.Errorf("invalid group registry JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return r, fmt.Errorf("unexpected registry content")
	}
	for i := range r.Groups {
		for j := range r.Groups[i].Properties {
			p := &r.Groups[i].Properties[j]
			if p.Config == "" {
				return r, fmt.Errorf("property config is required")
			}
			p.Hotel, err = config.Load(filepath.Join(filepath.Dir(path), p.Config))
			if err != nil {
				return r, fmt.Errorf("cannot load property config")
			}
		}
	}
	return r, r.Validate()
}

func (r Registry) Validate() error {
	if len(r.Groups) == 0 {
		return fmt.Errorf("groups are required")
	}
	ids, schemas := map[string]bool{}, map[string]bool{}
	for _, g := range r.Groups {
		if !identifier.MatchString(g.ID) || !identifier.MatchString(g.Schema) || g.Schema == "hotel_parser" || g.Schema == "public" || g.Schema == "information_schema" || strings.HasPrefix(g.Schema, "pg_") || ids[g.ID] || schemas[g.Schema] || len(g.Properties) == 0 {
			return fmt.Errorf("invalid/duplicate group or schema")
		}
		ids[g.ID], schemas[g.Schema] = true, true
		properties := map[string]bool{}
		for _, p := range g.Properties {
			if err := p.Hotel.Validate(); err != nil {
				return err
			}
			if properties[p.Hotel.ID] {
				return fmt.Errorf("duplicate property in group")
			}
			properties[p.Hotel.ID] = true
			if p.LOS < 0 || p.LOS > 3 {
				return fmt.Errorf("scheduled LOS must be 1, 2 or 3")
			}
			if p.LOS != 0 && !slices.Contains(p.Hotel.Collection.Nights, p.LOS) {
				return fmt.Errorf("legacy los must be enabled in collection.nights")
			}
			if p.LOS != 2 && p.LOS2Evidence != "" {
				return fmt.Errorf("los2_evidence requires LOS=2")
			}
		}
	}
	return nil
}
