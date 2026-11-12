package tv

import "strings"

// Preset is a ready-made removal profile. Applying a preset only ticks the
// matching apps in the list — the user still reviews and confirms the removal,
// and all the usual protections apply.
type Preset struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Vendors     []string `json:"vendors"`  // lower-case manufacturer substrings; empty = any TV
	Packages    []string `json:"packages"` // exact names, or prefixes ending with '*'
	// AllRecommended adds every app the knowledge base marks as "recommended".
	AllRecommended bool `json:"allRecommended"`
}

var foreignServices = []string{
	"com.netflix.ninja", "com.amazon.amazonvideo.livingroom", "com.disney.disneyplus",
	"com.google.android.videos", "com.google.android.youtube.tvmusic", "com.google.android.youtube.tvkids",
	"com.google.android.play.games",
}

var presets = []Preset{
	{
		ID: "safe", Title: "Безопасная очистка",
		Description:    "Всё, что база знаний считает рекламой, телеметрией, демо-режимом или промо-приложением. Ни одна функция телевизора не пропадёт. Подходит любому ТВ.",
		AllRecommended: true,
	},
	{
		ID: "xiaomi-ads", Title: "Xiaomi / Mi TV: без рекламы и слежки",
		Description: "Рекламные каналы PatchWall, аналитика и статистика Xiaomi, промо-контент. Сам PatchWall остаётся.",
		Vendors:     []string{"xiaomi", "redmi", "mi"},
		Packages: []string{
			"com.mitv.tvhome.mitvplus", "com.mitv.tvhome.michannel", "com.miui.tv.analytics", "com.xiaomi.statistic",
			"com.xm.webcontent", "com.xiaomi.mimusic2", "fusion.android.tv.demo", "com.android.dynsystem", "com.android.printspooler",
			"com.google.android.feedback", "com.google.android.tvtutorials",
		},
	},
	{
		ID: "xiaomi-max", Title: "Xiaomi / Mi TV: максимум скорости",
		Description: "То же, плюс сама оболочка PatchWall, её галерея, заставка и зарубежные сервисы. Сначала установите другой рабочий стол на вкладке «Установка» — программа не даст остаться без него.",
		Vendors:     []string{"xiaomi", "redmi", "mi"},
		Packages: append([]string{
			"com.mitv.tvhome.atv", "com.mitv.tvhome.mitvplus", "com.mitv.tvhome.michannel", "com.miui.tv.analytics",
			"com.xiaomi.statistic", "com.xm.webcontent", "com.xiaomi.mimusic2", "com.mitv.gallery", "com.mitv.dream",
			"fusion.android.tv.demo", "com.android.dynsystem", "com.android.printspooler",
			"com.google.android.feedback", "com.google.android.tvtutorials",
		}, foreignServices...),
	},
	{
		ID: "google-extras", Title: "Android TV / Google TV: лишние сервисы Google",
		Description: "YouTube Music, YouTube Kids, Play Фильмы, Play Игры, отправка отзывов и обучающие подсказки. YouTube, Play Маркет и голосовой поиск остаются.",
		Packages: []string{
			"com.google.android.youtube.tvmusic", "com.google.android.youtube.tvkids", "com.google.android.videos",
			"com.google.android.play.games", "com.google.android.feedback", "com.google.android.tvtutorials",
		},
	},
	{
		ID: "foreign", Title: "Сервисы, которые не работают в России",
		Description: "Netflix, Prime Video, Disney+, Play Фильмы и другие предустановленные зарубежные сервисы.",
		Packages:    foreignServices,
	},
	{
		ID: "retail", Title: "Демо-режим магазина и заглушки",
		Description: "Витринные демо-ролики, тестовые заглушки и режимы для разработчиков, которые остаются в прошивке после покупки.",
		Packages:    []string{"fusion.android.tv.demo", "com.android.dynsystem", "com.android.cts.*", "com.android.printspooler", "com.android.retaildemo"},
	},
}

// Presets returns all ready-made profiles and the id of the one that suits
// the manufacturer best.
func Presets(manufacturer string) (list []Preset, suggested string) {
	m := strings.ToLower(strings.TrimSpace(manufacturer))
	suggested = "safe"
	for _, p := range presets {
		for _, v := range p.Vendors {
			// "mi" must match the whole word only ("Mi TV" is reported as plain "Xiaomi" or "Mi").
			if (v == "mi" && m == "mi") || (v != "mi" && m != "" && strings.Contains(m, v)) {
				if suggested == "safe" {
					suggested = p.ID
				}
			}
		}
	}
	return presets, suggested
}

// Match reports whether pkg belongs to the preset. level is the package's
// level from the knowledge base (after device-specific adjustments).
func (p Preset) Match(pkg string, level Level) bool {
	if p.AllRecommended && level == Recommended {
		return true
	}
	for _, pat := range p.Packages {
		if matches(pat, pkg) {
			return true
		}
	}
	return false
}
