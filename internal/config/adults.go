package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// AdultCounts принимает прежнее adults: 2 и список независимых сценариев adults: [1, 2].
type AdultCounts []int

func (a *AdultCounts) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("adults: must be an integer or array")
	}
	var counts []int
	if len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '[' {
		if err := json.Unmarshal(data, &counts); err != nil {
			return err
		}
	} else {
		var count int
		if err := json.Unmarshal(data, &count); err != nil {
			return err
		}
		counts = []int{count}
	}
	*a = counts
	return nil
}

func (a AdultCounts) MarshalJSON() ([]byte, error) {
	if len(a) == 1 {
		return json.Marshal(a[0])
	}
	return json.Marshal([]int(a))
}
