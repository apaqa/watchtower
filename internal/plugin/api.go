// Plugin REST API 路由
package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
)

// RegisterRoutes 将插件管理 API 路由注册到给定的 ServeMux
func RegisterRoutes(mux *http.ServeMux, m *Manager) {
	// GET /api/v1/plugins — 列出所有插件及其状态
	mux.HandleFunc("/api/v1/plugins", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"仅支持 GET 方法"}`, http.StatusMethodNotAllowed)
			return
		}
		list := m.List()
		if list == nil {
			list = []PluginInfo{}
		}
		json.NewEncoder(w).Encode(list)
	})

	// POST /api/v1/plugins/{name}/stop  — 停止插件
	// POST /api/v1/plugins/{name}/start — 启动插件
	mux.HandleFunc("/api/v1/plugins/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"仅支持 POST 方法"}`, http.StatusMethodNotAllowed)
			return
		}

		// 路径格式：/api/v1/plugins/{name}/{action}
		tail := strings.TrimPrefix(r.URL.Path, "/api/v1/plugins/")
		parts := strings.SplitN(tail, "/", 2)
		if len(parts) != 2 {
			http.Error(w, `{"error":"路径格式应为 /api/v1/plugins/{name}/{start|stop}"}`, http.StatusBadRequest)
			return
		}
		name, action := parts[0], parts[1]

		switch action {
		case "stop":
			if err := m.StopPlugin(name); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case "start":
			if err := m.StartPlugin(name); err != nil {
				http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, `{"error":"未知操作，支持 start 或 stop"}`, http.StatusBadRequest)
		}
	})
}
