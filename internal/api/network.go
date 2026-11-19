package api

import (
	"net"
	"net/http"
	"runtime"
	"strings"

	"litepan/internal/settings"
)

// networkAddresses 返回本机可用于局域网访问的 IPv4 列表。
// 端口不在这里返回：客户端就在同一个端口上，用 window.location.port 拼即可。
func (h *Handler) networkAddresses(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{
		"lanAccessEnabled": h.settings.Bool(settings.KeyDesktopLanAccess),
		"ips":              lanIPv4s(),
		"platform":         runtime.GOOS,
	})
}

func lanIPv4s() []string {
	out := make([]string, 0, 4)
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Hyper-V 的 Default Switch 是给 WSL/虚拟机用的 NAT 网络，
		// 局域网里的手机、电视访问不到，列出来只会误导。
		if strings.Contains(ifc.Name, "Default Switch") {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out
}
