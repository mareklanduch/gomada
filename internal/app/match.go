package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mareklanduch/gomada/internal/omada"
)

// FormatMAC rewrites a MAC address in any common notation
// (aa:bb:cc:dd:ee:ff, AA-BB-..., aabb.ccdd.eeff, aabbccddeeff) into the
// controller's notation, AA-BB-CC-DD-EE-FF. It reports false when s is not a
// MAC address.
func FormatMAC(s string) (string, bool) {
	hex := strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.TrimSpace(s)))
	if len(hex) != 12 || strings.Trim(hex, "0123456789ABCDEF") != "" {
		return "", false
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, hex[i:i+2])
	}
	return strings.Join(parts, "-"), true
}

// find picks the one item that target names: by MAC address in any notation,
// or else by exact name, ignoring case. Several matches are an error that
// lists them, so a person can pick by MAC address.
func find[T any](kind, target string, items []T, mac func(T) string, names func(T) []string, describe func(T) string) (T, error) {
	var zero T
	if want, ok := FormatMAC(target); ok {
		for _, item := range items {
			if got, ok := FormatMAC(mac(item)); ok && got == want {
				return item, nil
			}
		}
	}
	var found []T
	for _, item := range items {
		for _, name := range names(item) {
			if name != "" && strings.EqualFold(name, target) {
				found = append(found, item)
				break
			}
		}
	}
	switch len(found) {
	case 0:
		return zero, fmt.Errorf("no %s matches %q", kind, target)
	case 1:
		return found[0], nil
	}
	list := make([]string, len(found))
	for i, item := range found {
		list[i] = describe(item)
	}
	return zero, fmt.Errorf("%q matches several %ss; use the MAC address: %s", target, kind, strings.Join(list, ", "))
}

// typeOrder puts gateways first, then switches, then access points.
var typeOrder = map[string]int{omada.TypeGateway: 0, omada.TypeSwitch: 1, omada.TypeAP: 2}

func sortDevices(devs []omada.Device) {
	rank := func(d omada.Device) int {
		if r, ok := typeOrder[d.Type]; ok {
			return r
		}
		return len(typeOrder)
	}
	sort.SliceStable(devs, func(i, j int) bool {
		if ri, rj := rank(devs[i]), rank(devs[j]); ri != rj {
			return ri < rj
		}
		return strings.ToLower(devs[i].Label()) < strings.ToLower(devs[j].Label())
	})
}

func sortClients(clients []omada.NetClient) {
	sort.SliceStable(clients, func(i, j int) bool {
		return strings.ToLower(clients[i].Label()) < strings.ToLower(clients[j].Label())
	})
}
