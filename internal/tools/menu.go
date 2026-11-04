package tools

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/i18n"
)

// MenuKeys are the left-navigation menu keys manageable by the tool center (in sidebar display order).
// The tool center itself is fixed at the sidebar bottom and is not in this list.
var MenuKeys = []string{"tasks", "workflows", "agents", "projects", "commands", "knowledge", "remote-channels", "skills", "apis", "settings"}

// MenuItem is a menu switch config item
type MenuItem struct {
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
	SortOrder int    `json:"sort_order"`
}

type menuConfig struct {
	SortOrder *int `json:"sort_order"`
}

type saveMenuItem struct {
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
	SortOrder *int   `json:"sort_order"`
}

// RegisterMenuRoutes registers the left-navigation menu switch config routes
func (h *Handler) RegisterMenuRoutes(r *gin.RouterGroup) {
	r.GET("/menu", h.listMenu)
	r.PUT("/menu", h.saveMenu)
}

// listMenu returns the on/off state of all manageable menus (default on if unconfigured)
func (h *Handler) listMenu(c *gin.Context) {
	rows, err := h.dbRef.Get().Query(
		`SELECT tool_key, enabled, config_json FROM gt_tool_settings WHERE tool_key IN (`+placeholders(len(MenuKeys))+`)`,
		toAnySlice(MenuKeys)...,
	)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "common_server_error", "")
		return
	}
	defer rows.Close()

	type storedMenuItem struct {
		enabled   bool
		sortOrder *int
	}
	storedItems := make(map[string]storedMenuItem, len(MenuKeys))
	for rows.Next() {
		var key string
		var enabled int
		var configJSON string
		if err := rows.Scan(&key, &enabled, &configJSON); err != nil {
			i18n.Error(c, http.StatusInternalServerError, "common_server_error", "")
			return
		}
		var config menuConfig
		_ = json.Unmarshal([]byte(configJSON), &config)
		storedItems[key] = storedMenuItem{enabled: enabled == 1, sortOrder: config.SortOrder}
	}
	if err := rows.Err(); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "common_server_error", "")
		return
	}

	items := make([]MenuItem, 0, len(MenuKeys))
	for index, key := range MenuKeys {
		stored, ok := storedItems[key]
		enabled := stored.enabled
		if !ok {
			enabled = true // Enabled by default
		}
		sortOrder := index
		if ok && stored.sortOrder != nil && *stored.sortOrder >= 0 {
			sortOrder = *stored.sortOrder
		}
		items = append(items, MenuItem{Key: key, Enabled: enabled, SortOrder: sortOrder})
	}
	// Identical or anomalous sort values fall back to the default order stably, ensuring old data still displays correctly.
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].SortOrder < items[j].SortOrder
	})
	// Return continuous indices externally; once the frontend writes them back, holes or duplicates in old data are fixed.
	for index := range items {
		items[index].SortOrder = index
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// saveMenu saves menu switch states in batch
func (h *Handler) saveMenu(c *gin.Context) {
	var req struct {
		Items []saveMenuItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		i18n.Error(c, http.StatusBadRequest, "common_request_invalid", "")
		return
	}
	if len(req.Items) == 0 {
		i18n.Error(c, http.StatusBadRequest, "tool_menu_items_required", "")
		return
	}

	now := time.Now().UnixMilli()
	tx, err := h.dbRef.Get().Begin()
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "common_server_error", "")
		return
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO gt_tool_settings (tool_key, enabled, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tool_key) DO UPDATE SET enabled = excluded.enabled, config_json = excluded.config_json, updated_at = excluded.updated_at`)
	if err != nil {
		i18n.Error(c, http.StatusInternalServerError, "common_server_error", "")
		return
	}
	defer stmt.Close()

	seen := make(map[string]bool, len(req.Items))
	seenOrder := make(map[int]bool, len(req.Items))
	for _, item := range req.Items {
		key := strings.TrimSpace(item.Key)
		if !containsString(MenuKeys, key) {
			i18n.Errorf(c, http.StatusBadRequest, "tool_menu_key_unknown", i18n.Params{"Key": key})
			return
		}
		if seen[key] {
			i18n.Errorf(c, http.StatusBadRequest, "tool_menu_key_duplicate", i18n.Params{"Key": key})
			return
		}
		seen[key] = true
		sortOrder := defaultMenuOrder(key)
		if item.SortOrder != nil {
			sortOrder = *item.SortOrder
		}
		if sortOrder < 0 || sortOrder >= len(MenuKeys) {
			i18n.Errorf(c, http.StatusBadRequest, "tool_menu_order_invalid", i18n.Params{"Key": key})
			return
		}
		if seenOrder[sortOrder] {
			i18n.Error(c, http.StatusBadRequest, "tool_menu_order_duplicate", "")
			return
		}
		seenOrder[sortOrder] = true
		enabled := 0
		if item.Enabled {
			enabled = 1
		}
		configJSON, err := json.Marshal(menuConfig{SortOrder: &sortOrder})
		if err != nil {
			i18n.Error(c, http.StatusInternalServerError, "common_server_error", "")
			return
		}
		if _, err := stmt.Exec(key, enabled, string(configJSON), now, now); err != nil {
			i18n.Error(c, http.StatusInternalServerError, "common_server_error", "")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		i18n.Error(c, http.StatusInternalServerError, "common_server_error", "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func defaultMenuOrder(key string) int {
	for index, menuKey := range MenuKeys {
		if menuKey == key {
			return index
		}
	}
	return -1
}

// placeholders generates n placeholders, e.g. n=3 -> "?,?,?"; returns an empty string when n<=0.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

// toAnySlice converts []string to []any for passing as SQL parameters
func toAnySlice(keys []string) []any {
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	return args
}

// containsString reports whether a slice contains the target string
func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}
