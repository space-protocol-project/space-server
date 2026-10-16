package home

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"time"
)

func appendAutomaticChecks(report *DoctorReport, data string, now time.Time) {
	if data == "" {
		return
	}
	content, err := readDoctorFile(filepath.Join(data, "automatic-network.json"))
	var status AutomaticNetworkStatus
	if err != nil || json.Unmarshal(content, &status) != nil || status.Format != 1 {
		return
	}
	age := now.Sub(time.UnixMilli(status.At))
	if age < 0 || age > 45*time.Second {
		return
	}
	switch status.Code {
	case "mapped":
		endpoint, err := netip.ParseAddrPort(status.External)
		if err != nil || !endpoint.Addr().Is4() || endpoint.Port() == 0 || status.ExpiresAt <= now.UnixMilli() {
			return
		}
		if status.Protocol != "PCP" && status.Protocol != "NAT-PMP" && status.Protocol != "UPnP" {
			return
		}
		report.add("router_mapping_ok", "ok", status.Protocol+" · "+endpoint.String())
	case "no_gateway", "mapping_failed", "renewal_failed", "configuration_failed":
		report.add("router_mapping_failed", "warning", "")
	}
	if status.IPCode == "unavailable" || status.IPCode == "configuration_failed" {
		report.add("automatic_ip_failed", "warning", "")
	}
}
