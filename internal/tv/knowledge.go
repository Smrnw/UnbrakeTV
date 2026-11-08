package tv

import "strings"

// Level describes how safe it is to remove a package.
type Level string

const (
	// Critical: removing it can break the system (boot loop, no picture, no settings).
	// Requires typing a confirmation phrase.
	Critical Level = "critical"
	// Caution: the TV keeps working but loses a feature (voice search, updates, live TV...).
	Caution Level = "caution"
	// Recommended: ads, telemetry, demo modes, preinstalled promo apps.
	Recommended Level = "recommended"
	// Optional: a normal app; remove it if you don't use it.
	Optional Level = "optional"
	// Unknown: a system component we know nothing about.
	Unknown Level = "unknown"
)

// Info is what we know about a package.
type Info struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Level  Level  `json:"level"`
}

type rule struct {
	pattern string // exact name, or prefix when it ends with '*'
	Info
}

func r(p string, l Level, name, reason string) rule {
	return rule{pattern: p, Info: Info{Name: name, Reason: reason, Level: l}}
}

// rules are checked in order; the first match wins, so exact names go before wildcards.
var rules = []rule{
	// ---------- Ядро Android ----------
	r("android", Critical, "Ядро Android", "Основа системы. Без неё телевизор не загрузится."),
	r("com.android.systemui", Critical, "Системный интерфейс", "Отвечает за весь системный интерфейс."),
	r("com.android.shell", Critical, "Оболочка ADB", "Через неё работает эта программа."),
	r("com.android.settings", Critical, "Настройки", "Без неё не открыть настройки."),
	r("com.android.tv.settings", Critical, "Настройки ТВ", "Без неё не открыть настройки телевизора."),
	r("com.android.providers.*", Critical, "Системное хранилище данных", "Хранит настройки, медиа, загрузки, каналы. Удаление ломает систему."),
	r("com.android.networkstack*", Critical, "Сетевой стек", "Без него пропадёт Wi-Fi и интернет."),
	r("com.android.wifi*", Critical, "Wi-Fi", "Без него пропадёт Wi-Fi."),
	r("com.android.bluetooth*", Caution, "Bluetooth", "Пропадут Bluetooth-пульт, наушники и колонки."),
	r("com.android.inputdevices", Critical, "Устройства ввода", "Нужно для пульта и клавиатур."),
	r("com.android.packageinstaller", Critical, "Установщик приложений", "Без него не установить и не обновить приложения."),
	r("com.google.android.packageinstaller", Critical, "Установщик приложений", "Без него не установить и не обновить приложения."),
	r("com.android.permissioncontroller", Critical, "Управление разрешениями", "Нужно для работы любых приложений."),
	r("com.google.android.permissioncontroller", Critical, "Управление разрешениями", "Нужно для работы любых приложений."),
	r("com.android.se", Critical, "Защищённый элемент", "Системный компонент безопасности."),
	r("com.android.keychain", Critical, "Хранилище сертификатов", "Без него не работают HTTPS и вход в аккаунты."),
	r("com.android.certinstaller", Caution, "Установка сертификатов", "Нужно для установки сертификатов."),
	r("com.android.location.fused", Critical, "Определение местоположения", "На многих ТВ отключение приводит к бесконечной перезагрузке."),
	r("com.android.externalstorage", Critical, "Внешние накопители", "Без него не видны флешки и память."),
	r("com.android.mtp", Caution, "Подключение по USB", "Нужно для передачи файлов по USB."),
	r("com.android.vpndialogs", Caution, "VPN", "Без него не работает VPN."),
	r("com.android.captiveportallogin", Caution, "Вход в Wi-Fi сети", "Нужно для гостевых сетей со страницей входа."),
	r("com.android.proxyhandler", Caution, "Прокси", "Нужно, если используете прокси."),
	r("com.android.pacprocessor", Caution, "Автонастройка прокси", "Нужно, если используете прокси."),
	r("com.android.localtransport", Caution, "Резервное копирование", "Системный компонент резервного копирования."),
	r("com.android.backupconfirm", Caution, "Подтверждение бэкапа", "Системный диалог резервного копирования."),
	r("com.android.sharedstoragebackup", Caution, "Резервное копирование", "Системный компонент резервного копирования."),
	r("com.android.wallpaperbackup", Caution, "Резервное копирование обоев", "Системный компонент."),
	r("com.android.companiondevicemanager", Caution, "Связанные устройства", "Нужно для подключения телефона и аксессуаров."),
	r("com.android.statementservice", Caution, "Проверка ссылок", "Системный компонент."),
	r("com.android.dynsystem", Recommended, "Dynamic System Updates", "Режим для разработчиков, на ТВ не нужен."),
	r("com.android.cts.*", Recommended, "Заглушка для тестов CTS", "Нужна только при сертификации прошивки."),
	r("com.android.printspooler", Recommended, "Печать", "Печатать с телевизора никто не будет."),
	r("com.android.htmlviewer", Optional, "Просмотр HTML", "Почти никогда не используется."),
	r("com.android.dreams.basic", Optional, "Базовая заставка", "Простая заставка."),
	r("com.android.soundpicker", Optional, "Выбор звуков", "Почти не используется."),
	r("com.android.hotspot2.osulogin", Optional, "Passpoint Wi-Fi", "Вход в операторские Wi-Fi сети, дома не нужен."),
	r("com.android.settings.intelligence", Caution, "Поиск по настройкам", "Пропадёт поиск в настройках."),
	r("com.android.tv.remote*", Caution, "Управление с телефона", "Не будет работать пульт в приложении на телефоне."),
	r("com.android.vending", Critical, "Google Play Маркет", "Без него не установить и не обновить приложения."),
	r("com.android.*", Unknown, "Компонент Android", "Системный компонент Android."),

	// ---------- Google ----------
	r("com.google.android.gms", Critical, "Сервисы Google Play", "Без них не работают Play Маркет, YouTube, вход в аккаунт."),
	r("com.google.android.gsf", Critical, "Google Services Framework", "Без него не работают сервисы Google."),
	r("com.android.vending", Critical, "Google Play Маркет", "Без него не установить и не обновить приложения."),
	r("com.google.android.webview", Critical, "Android System WebView", "Нужен приложениям для показа веб-страниц."),
	r("com.google.android.ext.*", Critical, "Сервисы Android", "Системный модуль Android."),
	r("com.google.android.modulemetadata", Critical, "Метаданные модулей", "Системный модуль Android."),
	r("com.google.android.overlay.*", Critical, "Системный оверлей", "Настройки системных модулей."),
	r("com.google.android.tv.frameworkpackagestubs", Critical, "Заглушки системы", "Системный компонент Android TV."),
	r("com.google.android.tvlauncher", Caution, "Главный экран Android TV", "Стандартный рабочий стол. Удаляйте только при наличии другого лаунчера."),
	r("com.google.android.apps.tv.launcherx", Critical, "Главный экран Google TV", "На Google TV 14 отключение может привести к чёрному экрану при загрузке, даже если стоит другой лаунчер."),
	r("com.google.android.tvrecommendations", Caution, "Рекомендации главного экрана", "Нужно стандартному рабочему столу."),
	r("com.google.android.katniss", Caution, "Голосовой поиск Google", "Перестанет работать кнопка микрофона."),
	r("com.google.android.apps.mediashell", Caution, "Chromecast built-in", "Перестанет работать трансляция с телефона."),
	r("com.google.android.tv.remote.service", Caution, "Сервис пульта Google", "Не будет работать пульт с телефона."),
	r("com.google.android.inputmethod.latin", Caution, "Клавиатура Gboard", "Если это единственная клавиатура — нечем будет вводить текст."),
	r("com.google.android.leanbacklauncher.*", Caution, "Компоненты рабочего стола", "Нужно стандартному рабочему столу."),
	r("com.google.android.tungsten.setupwraith", Caution, "Мастер первой настройки", "Нужен при сбросе к заводским настройкам."),
	r("com.google.android.partnersetup", Caution, "Настройка партнёра", "Системный компонент Google."),
	r("com.google.android.onetimeinitializer", Caution, "Первичная инициализация", "Системный компонент Google."),
	r("com.google.android.sss.authbridge", Caution, "Мост авторизации", "Системный компонент Google."),
	r("com.google.android.syncadapters.*", Caution, "Синхронизация Google", "Синхронизация данных аккаунта."),
	r("com.google.android.tts", Optional, "Синтез речи Google", "Нужен только для озвучивания экрана."),
	r("com.google.android.marvin.talkback", Optional, "TalkBack", "Озвучивание экрана для незрячих."),
	r("com.google.android.backdrop", Optional, "Заставка Google", "Фоновые фотографии в режиме ожидания."),
	r("com.google.android.youtube.tv", Optional, "YouTube", "Удаляйте, только если не смотрите YouTube."),
	r("com.google.android.youtube.tvmusic", Recommended, "YouTube Music", "Предустановленный сервис, мало кто пользуется."),
	r("com.google.android.youtube.tvkids", Recommended, "YouTube Kids", "Предустановленный сервис."),
	r("com.google.android.videos", Recommended, "Google TV / Play Фильмы", "Магазин фильмов Google, в РФ не работает."),
	r("com.google.android.play.games", Recommended, "Google Play Игры", "Почти никем не используется на ТВ."),
	r("com.google.android.feedback", Recommended, "Отправка отзывов", "Отправляет отчёты Google."),
	r("com.google.android.tvtutorials", Recommended, "Обучение", "Подсказки для новичков."),
	r("com.google.android.apps.tv.dreamx", Optional, "Заставка Google TV", "Фоновые фотографии."),
	r("com.google.*", Unknown, "Компонент Google", "Компонент Google."),

	// ---------- Чипсеты (MediaTek, MStar, Amlogic, Realtek) ----------
	r("com.mediatek.tv.service", Critical, "ТВ-сервис MediaTek", "Отвечает за изображение и тюнер."),
	r("com.mediatek.tvinput", Critical, "Входы ТВ (HDMI, антенна)", "Без него пропадут HDMI и эфир."),
	r("com.mediatek.tv.agent", Critical, "Агент ТВ MediaTek", "Системный сервис телевизора."),
	r("com.mediatek.network", Critical, "Сеть MediaTek", "Без него может пропасть сеть."),
	r("com.mediatek.wwtv.setupwizard", Caution, "Мастер настройки ТВ", "Нужен при первой настройке."),
	r("com.mediatek.wwtv.webview", Caution, "WebView MediaTek", "Нужен системным страницам."),
	r("com.mediatek.android.boot.appsplashscreen", Caution, "Заставка загрузки", "Экран при включении."),
	r("mediatek.factorymenu.ui", Caution, "Сервисное меню", "Инженерное меню, обычно не мешает."),
	r("com.mediatek.*", Caution, "Компонент MediaTek", "Системный компонент процессора телевизора."),
	r("com.mstar.*", Caution, "Компонент MStar", "Системный компонент процессора телевизора."),
	r("com.droidlogic.*", Caution, "Компонент Amlogic", "Системный компонент процессора телевизора."),
	r("com.amlogic.*", Caution, "Компонент Amlogic", "Системный компонент процессора телевизора."),
	r("com.realtek.*", Caution, "Компонент Realtek", "Системный компонент процессора телевизора."),
	r("com.dolby.*", Caution, "Dolby Audio", "Пропадёт обработка звука Dolby."),
	r("fusion.android.tv.demo", Recommended, "Демо-режим магазина", "Нужен только на витрине."),

	// ---------- Xiaomi ----------
	r("mitv.service", Critical, "Системный сервис Xiaomi", "Ядро прошивки Xiaomi."),
	r("com.mitv.tvhome.atv", Caution, "PatchWall (оболочка Xiaomi)", "Тяжёлая фирменная оболочка с рекламой. Удаляйте, если есть другой рабочий стол."),
	r("com.mitv.tvhome.mitvplus", Recommended, "Mi TV+ (каналы Xiaomi)", "Рекламные каналы PatchWall."),
	r("com.mitv.tvhome.michannel", Recommended, "Каналы PatchWall", "Рекомендации Xiaomi на главном экране."),
	r("com.miui.tv.analytics", Recommended, "Аналитика Xiaomi", "Собирает статистику использования."),
	r("com.xiaomi.statistic", Recommended, "Статистика Xiaomi", "Собирает и отправляет статистику."),
	r("com.xm.webcontent", Recommended, "Рекламный контент Xiaomi", "Подгружает промо-контент."),
	r("com.xiaomi.mimusic2", Recommended, "Mi Музыка", "Предустановленный плеер, редко нужен."),
	r("com.mitv.gallery", Optional, "Галерея Xiaomi", "Просмотр фото с флешки."),
	r("com.mitv.dream", Optional, "Заставка Xiaomi", "Есть стандартная заставка."),
	r("com.mitv.videoplayer", Optional, "Видеоплеер Xiaomi", "Нужен для видео с флешки."),
	r("com.xiaomi.mitv.mediaexplorer", Optional, "Проводник Xiaomi", "Нужен для файлов с флешки."),
	r("com.mitv.livetv", Caution, "Эфирное ТВ Xiaomi", "Пропадёт просмотр антенны и кабеля."),
	r("com.mitv.livetv.*", Caution, "Модуль эфирного ТВ", "Нужен для CI-модулей и эфира."),
	r("com.mitv.upgrader", Caution, "Обновление прошивки", "Перестанут приходить обновления."),
	r("com.xiaomi.mitv.updateservice", Caution, "Сервис обновлений", "Перестанут приходить обновления."),
	r("com.xiaomi.tvqs.overseas", Caution, "Быстрые настройки", "Перестанет работать кнопка быстрых настроек."),
	r("com.mi.bluetooth", Caution, "Bluetooth-пульт Xiaomi", "Может перестать работать пульт."),
	r("com.mitv.milinkservice", Caution, "Трансляция Mi Link", "Пропадёт трансляция с телефонов Xiaomi."),
	r("com.xiaomi.mitv.smartshare", Caution, "Smart Share", "Пропадёт трансляция с телефона."),
	r("com.xiaomi.wfdsinkhelperservice", Caution, "Miracast", "Пропадёт беспроводной экран."),
	r("com.mitv.overseaservice", Caution, "Сервисы Xiaomi", "Системные сервисы прошивки."),
	r("com.xiaomi.mitv.tvmanager", Caution, "Менеджер ТВ", "Очистка и обслуживание."),
	r("com.mitv.toolhouse", Caution, "Инструменты Xiaomi", "Служебные функции прошивки."),
	r("com.xiaomo.tv.milegal", Caution, "Юридические документы", "Соглашения Xiaomi."),
	r("com.xiaomi.android.tvsetup.partnercustomizer", Caution, "Настройка прошивки Xiaomi", "Применяет настройки производителя."),
	r("android.autoinstalls.config.*", Caution, "Автоустановка приложений", "Ставит промо-приложения при сбросе."),
	r("com.mitv.*", Unknown, "Компонент Xiaomi", "Компонент прошивки Xiaomi."),
	r("com.xiaomi.*", Unknown, "Компонент Xiaomi", "Компонент прошивки Xiaomi."),
	r("com.miui.*", Unknown, "Компонент Xiaomi", "Компонент прошивки Xiaomi."),

	// ---------- Sony / Philips / TCL / Hisense ----------
	r("com.sony.dtv.tvx", Critical, "ТВ-сервис Sony", "Отвечает за изображение."),
	r("com.sony.dtv.*", Caution, "Компонент Sony", "Компонент прошивки Sony."),
	r("org.droidtv.*", Caution, "Компонент Philips", "Компонент прошивки Philips."),
	r("com.tcl.*", Caution, "Компонент TCL", "Компонент прошивки TCL."),
	r("com.hisense.*", Caution, "Компонент Hisense", "Компонент прошивки Hisense."),

	// ---------- Предустановленные сервисы ----------
	r("com.netflix.ninja", Recommended, "Netflix", "В РФ не работает."),
	r("com.amazon.amazonvideo.livingroom", Recommended, "Prime Video", "В РФ почти не используется."),
	r("com.disney.disneyplus", Recommended, "Disney+", "В РФ не работает."),
	r("com.spotify.tv.android", Optional, "Spotify", "Музыкальный сервис."),
	r("ru.mts.mtstv", Optional, "МТС ТВ / KION", "Онлайн-кинотеатр."),
	r("ru.rt.video.app.tv", Optional, "Wink", "Онлайн-кинотеатр Ростелекома."),
	r("ru.ivi.client", Optional, "ivi", "Онлайн-кинотеатр."),
	r("ru.more.play", Optional, "more.tv", "Онлайн-кинотеатр."),
	r("ru.start.androidmobile", Optional, "START", "Онлайн-кинотеатр."),
	r("gpm.tnt_premier", Optional, "PREMIER", "Онлайн-кинотеатр."),
	r("ru.rutube.app", Optional, "Rutube", "Видеосервис."),
	r("com.vk.tv", Optional, "VK Видео", "Видеосервис."),
	r("ru.kinopoisk.tv", Optional, "Кинопоиск", "Онлайн-кинотеатр."),
	r("ru.okko.tv", Optional, "Okko", "Онлайн-кинотеатр."),
}

// telemetryHints catch unknown system packages that look like ads/telemetry.
var telemetryHints = []string{"analytics", "statistic", "telemetry", "tracker", "advert", ".ads.", "demo", "retail", "feedback"}

// Lookup returns what we know about pkg. thirdParty marks user-installed apps.
func Lookup(pkg string, thirdParty bool) Info {
	for _, ru := range rules {
		if matches(ru.pattern, pkg) {
			inf := ru.Info
			// For wildcard matches keep the package name visible.
			if strings.HasSuffix(ru.pattern, "*") && inf.Level == Unknown {
				inf.Name = pkg
			}
			return inf
		}
	}
	if thirdParty {
		return Info{Name: pkg, Reason: "Установленное приложение. Удалите, если не пользуетесь.", Level: Optional}
	}
	if strings.Contains(pkg, "auto_generated_rro") {
		return Info{Name: "Системный оверлей", Reason: "Настройки ресурсов прошивки. Удаление может сломать систему.", Level: Critical}
	}
	low := strings.ToLower(pkg)
	for _, h := range telemetryHints {
		if strings.Contains(low, h) {
			return Info{Name: pkg, Reason: "Похоже на телеметрию, рекламу или демо-режим.", Level: Recommended}
		}
	}
	return Info{Name: pkg, Reason: "Системный компонент производителя, о котором ничего не известно. Удаляйте только если уверены.", Level: Unknown}
}

func matches(pattern, pkg string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(pkg, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == pkg
}
