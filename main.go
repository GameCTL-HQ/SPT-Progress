// spt-progress — standalone read-only website for SPT (Single Player Tarkov)
// server profiles. Points at the server's user/profiles directory and shows
// every account's PMC progress: level, experience, stash money by currency,
// quest counts, skills. No database and no writes — safe to mount the live
// profiles dir read-only (NFS RWX allows the concurrent mount).
//
// Env:
//
//	GAMECTL_PROFILES_DIR — profiles dir (default /data/profiles)
//	SITE_BRAND           — owner name in title/header/footer (default GameCTL)
//	LISTEN_ADDR          — default :8080
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var tplFS embed.FS

var (
	profilesDir = envOr("GAMECTL_PROFILES_DIR", "/data/profiles")
	siteBrand   = envOr("SITE_BRAND", "GameCTL")
	cacheTTL    = 30 * time.Second
)

func envOr(k, dflt string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return dflt
}

// Currency item templates (EFT item tpl ids).
var currencyTpl = map[string]string{
	"5449016a4bdc2d6f028b456f": "RUB",
	"5696686a4bdc2da3298b456a": "USD",
	"569668774bdc2da2298b4568": "EUR",
}

// EFT quest status enum (numeric in profile JSON).
const (
	questStarted            = 2
	questAvailableForFinish = 3
	questSuccess            = 4
)

type skillEntry struct {
	Id       string  `json:"Id"`
	Progress float64 `json:"Progress"`
}

type profileFile struct {
	Info struct {
		Id       string `json:"id"`
		Username string `json:"username"`
		Edition  string `json:"edition"`
	} `json:"info"`
	Characters struct {
		Pmc struct {
			KarmaValue float64 `json:"karmaValue"`
			Info       struct {
				Nickname   string `json:"Nickname"`
				Side       string `json:"Side"`
				Level      int    `json:"Level"`
				Experience int64  `json:"Experience"`
			} `json:"Info"`
			Inventory struct {
				Items []struct {
					Tpl string `json:"_tpl"`
					Upd struct {
						StackObjectsCount int64 `json:"StackObjectsCount"`
					} `json:"upd"`
				} `json:"items"`
			} `json:"Inventory"`
			Skills struct {
				Common []skillEntry `json:"Common"`
			} `json:"Skills"`
			Quests []struct {
				Status json.Number `json:"status"`
			} `json:"Quests"`
			Stats struct {
				Eft struct {
					OverallCounters struct {
						Items []struct {
							Key   []string `json:"Key"`
							Value int64    `json:"Value"`
						} `json:"Items"`
					} `json:"OverallCounters"`
				} `json:"Eft"`
			} `json:"Stats"`
		} `json:"pmc"`
		Scav struct {
			Info struct {
				Level int `json:"Level"`
			} `json:"Info"`
		} `json:"scav"`
	} `json:"characters"`
	Spt struct {
		Version string `json:"version"`
	} `json:"spt"`
}

type skillRow struct {
	Name  string
	Level int
}

type playerRow struct {
	Id, Username, Nickname, Side, Edition, SptVersion string
	Level, ScavLevel                                  int
	Experience                                        int64
	Rub, Usd, Eur                                     int64
	QuestsDone, QuestsActive                          int
	FenceRep                                          float64
	Raids, SurvivedRaids, Kills, Deaths               int64
	TopSkills                                         []skillRow
	UpdatedAt                                         time.Time
}

var (
	cacheMu    sync.RWMutex
	cachedRows []playerRow
	cachedAt   time.Time
)

func counterVal(items []struct {
	Key   []string `json:"Key"`
	Value int64    `json:"Value"`
}, want ...string) int64 {
	var total int64
outer:
	for _, it := range items {
		for _, w := range want {
			ok := false
			for _, k := range it.Key {
				if k == w {
					ok = true
					break
				}
			}
			if !ok {
				continue outer
			}
		}
		total += it.Value
	}
	return total
}

func loadProfiles() []playerRow {
	cacheMu.RLock()
	if cachedRows != nil && time.Since(cachedAt) < cacheTTL {
		r := cachedRows
		cacheMu.RUnlock()
		return r
	}
	cacheMu.RUnlock()
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cachedRows != nil && time.Since(cachedAt) < cacheTTL {
		return cachedRows
	}
	ents, err := os.ReadDir(profilesDir)
	if err != nil {
		slog.Warn("read profiles dir failed", "dir", profilesDir, "err", err)
		cachedRows, cachedAt = []playerRow{}, time.Now()
		return cachedRows
	}
	rows := []playerRow{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(profilesDir, e.Name())
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var p profileFile
		if err := json.Unmarshal(raw, &p); err != nil {
			slog.Warn("parse profile failed", "file", e.Name(), "err", err)
			continue
		}
		if p.Info.Username == "" && p.Characters.Pmc.Info.Nickname == "" {
			continue // not a player profile
		}
		row := playerRow{
			Id:         strings.TrimSuffix(e.Name(), ".json"),
			Username:   p.Info.Username,
			Nickname:   p.Characters.Pmc.Info.Nickname,
			Side:       p.Characters.Pmc.Info.Side,
			Edition:    p.Info.Edition,
			SptVersion: p.Spt.Version,
			Level:      p.Characters.Pmc.Info.Level,
			ScavLevel:  p.Characters.Scav.Info.Level,
			Experience: p.Characters.Pmc.Info.Experience,
			FenceRep:   p.Characters.Pmc.KarmaValue,
		}
		if fi, err := e.Info(); err == nil {
			row.UpdatedAt = fi.ModTime()
		}
		for _, it := range p.Characters.Pmc.Inventory.Items {
			switch currencyTpl[it.Tpl] {
			case "RUB":
				row.Rub += it.Upd.StackObjectsCount
			case "USD":
				row.Usd += it.Upd.StackObjectsCount
			case "EUR":
				row.Eur += it.Upd.StackObjectsCount
			}
		}
		for _, q := range p.Characters.Pmc.Quests {
			if v, err := q.Status.Int64(); err == nil {
				switch v {
				case questSuccess:
					row.QuestsDone++
				case questStarted, questAvailableForFinish:
					row.QuestsActive++
				}
			}
		}
		oc := p.Characters.Pmc.Stats.Eft.OverallCounters.Items
		row.Raids = counterVal(oc, "Sessions", "Pmc")
		row.SurvivedRaids = counterVal(oc, "ExitStatus", "Survived", "Pmc")
		row.Kills = counterVal(oc, "Kills")
		row.Deaths = counterVal(oc, "ExitStatus", "Killed", "Pmc")
		skills := append([]skillEntry(nil), p.Characters.Pmc.Skills.Common...)
		sort.Slice(skills, func(i, j int) bool { return skills[i].Progress > skills[j].Progress })
		for _, s := range skills {
			if len(row.TopSkills) >= 8 || s.Progress <= 0 {
				break
			}
			row.TopSkills = append(row.TopSkills, skillRow{Name: s.Id, Level: int(s.Progress / 100)})
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Experience > rows[j].Experience })
	cachedRows, cachedAt = rows, time.Now()
	return rows
}

var tpl *template.Template

func render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, ok := data["Brand"]; !ok {
		data["Brand"] = siteBrand
	}
	if err := tpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("render failed", "tpl", name, "err", err)
		http.Error(w, "render failed", 500)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	funcs := template.FuncMap{
		"comma": func(n int64) string {
			s := fmt.Sprintf("%d", n)
			if n < 0 {
				return s
			}
			var out []byte
			for i, c := range []byte(s) {
				if i > 0 && (len(s)-i)%3 == 0 {
					out = append(out, ',')
				}
				out = append(out, c)
			}
			return string(out)
		},
		"since": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%dm ago", int(d.Minutes()))
			case d < 48*time.Hour:
				return fmt.Sprintf("%dh ago", int(d.Hours()))
			default:
				return fmt.Sprintf("%dd ago", int(d.Hours()/24))
			}
		},
		"pct": func(a, b int64) int {
			if b == 0 {
				return 0
			}
			return int(a * 100 / b)
		},
	}
	var err error
	tpl, err = template.New("").Funcs(funcs).ParseFS(tplFS, "templates/*.html")
	if err != nil {
		slog.Error("parse templates", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/player/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/player/")
		for _, row := range loadProfiles() {
			if row.Id == id {
				render(w, "player.html", map[string]any{"Title": row.DisplayName(), "P": row})
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		render(w, "home.html", map[string]any{"Title": "Players", "Rows": loadProfiles()})
	})

	addr := envOr("LISTEN_ADDR", ":8080")
	slog.Info("spt-progress up", "addr", addr, "profiles", profilesDir, "brand", siteBrand)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}
}

func (p playerRow) DisplayName() string {
	if p.Nickname != "" {
		return p.Nickname
	}
	return p.Username
}
