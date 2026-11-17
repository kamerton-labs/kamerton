// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package generator

import (
	"html"
	"strings"
	"testing"
	"time"

	"github.com/kamerton-labs/kamerton/internal/description"
)

func TestOffsetLabel(t *testing.T) {
	for in, want := range map[int]string{0: "UTC", 5 * 3600: "UTC+5", -3 * 3600: "UTC-3", 19800: "UTC+5:30", 20700: "UTC+5:45", -12600: "UTC-3:30", 14 * 3600: "UTC+14"} {
		if got := OffsetLabel(in); got != want {
			t.Errorf("OffsetLabel(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestZoneClockSegments(t *testing.T) {
	if c := newZoneClock("", testNow); len(c.segments) != 1 || c.segments[0].offset != 0 {
		t.Errorf("unset: %+v", c.segments)
	}
	if c := newZoneClock("Asia/Almaty", testNow); len(c.segments) != 1 || c.segments[0].offset != 5*3600 {
		// Kazakhstan moved to UTC+5 on 2024-03-01, more than a year before testNow.
		t.Errorf("Asia/Almaty: %+v", c.segments)
	}
	c := newZoneClock("Europe/Berlin", testNow)
	if len(c.segments) < 8 {
		t.Fatalf("Europe/Berlin: want a DST change twice a year over four years, got %+v", c.segments)
	}
	for i, s := range c.segments {
		if s.offset != 3600 && s.offset != 7200 {
			t.Errorf("Europe/Berlin segment %d: offset %d", i, s.offset)
		}
		if i > 0 && s.offset == c.segments[i-1].offset {
			t.Errorf("Europe/Berlin segment %d repeats the offset", i)
		}
	}
}

// shown is a message as the reader sees it: html/template writes "+" as &#43;,
// which Telegram and mail clients display as "+".
func shown(t *testing.T, tpl string, d amData) string {
	t.Helper()
	out := execHTML(t, tpl, d)
	checkTelegramHTML(t, out)
	return html.UnescapeString(out)
}

func zoneData(status string, starts ...time.Time) amData {
	d := sampleData(len(starts), status)
	for i, s := range starts {
		d.Alerts[i].StartsAt, d.Alerts[i].EndsAt = s, s.Add(10*time.Minute)
	}
	return d
}

func TestNotificationTimesInNamedZone(t *testing.T) {
	m := buildMessages("ru", "", newZoneClock("Asia/Almaty", testNow))
	d := sampleData(1, "firing") // started 14:03 UTC
	d.Alerts[0].Annotations[AnnotationMessageTitle] = "Оплата не проходит с {since}"
	d.Alerts[0].Annotations[AnnotationMessageText] = "Началось в {since}"
	d.Alerts[0].Annotations[AnnotationMessageAction] = "Проверьте, что было в {since}"
	tg := shown(t, m.TelegramHTML, d)
	for _, want := range []string{"Оплата не проходит с 19:03 15.11 UTC+5", "Началось в 19:03 15.11 UTC+5", "👉 Проверьте, что было в 19:03 15.11 UTC+5", "с 19:03 15.11 UTC+5\n"} {
		if !strings.Contains(tg, want) {
			t.Errorf("telegram lacks %q:\n%s", want, tg)
		}
	}
	if strings.Contains(tg, "14:03") {
		t.Errorf("UTC time left in the message:\n%s", tg)
	}
	if txt := execText(t, m.EmailText, d); !strings.Contains(txt, "с 19:03 15.11 UTC+5") || !strings.Contains(txt, "Началось в 19:03 15.11 UTC+5") {
		t.Errorf("email text:\n%s", txt)
	}
	if h := html.UnescapeString(execHTML(t, m.EmailHTML, d)); !strings.Contains(h, "19:03 15.11 UTC+5") {
		t.Errorf("email html:\n%s", h)
	}
	r := shown(t, m.TelegramHTML, sampleData(1, "resolved"))
	if !strings.Contains(r, "с 19:03 15.11 UTC+5 до 19:13 15.11 UTC+5") {
		t.Errorf("resolved:\n%s", r)
	}
}

func TestNotificationTimesUnsetZoneIsUTC(t *testing.T) {
	m := buildMessages("en", "", newZoneClock("", testNow))
	d := sampleData(1, "firing")
	d.Alerts[0].Annotations[AnnotationMessageTitle] = "Down since {since}"
	out := shown(t, m.TelegramHTML, d)
	if !strings.Contains(out, "Down since 14:03 15.11 UTC") || !strings.Contains(out, "since 14:03 15.11 UTC\n") {
		t.Errorf("unset zone:\n%s", out)
	}
}

func TestNotificationTimesAcrossDaylightSaving(t *testing.T) {
	m := buildMessages("ru", "", newZoneClock("Europe/Berlin", testNow))
	starts := []time.Time{
		time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC),  // before the spring change: CET
		time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC),  // after it (01:00 UTC): CEST
		time.Date(2026, 11, 15, 14, 3, 0, 0, time.UTC), // winter
		time.Date(2027, 7, 1, 12, 0, 0, 0, time.UTC),   // next summer
		time.Date(2028, 10, 29, 0, 59, 0, 0, time.UTC), // just before the autumn change of 2028
		time.Date(2028, 10, 29, 1, 0, 0, 0, time.UTC),  // at it
	}
	out := shown(t, m.TelegramHTML, zoneData("firing", starts...))
	for _, want := range []string{
		"с 01:30 29.03 UTC+1",
		"с 03:30 29.03 UTC+2",
		"с 15:03 15.11 UTC+1",
		"с 14:00 01.07 UTC+2",
		"с 02:59 29.10 UTC+2",
		"с 02:00 29.10 UTC+1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "Много ошибочных запросов") != len(starts) {
		t.Errorf("grouped message:\n%s", out)
	}
	r := execText(t, m.EmailText, zoneData("resolved", time.Date(2026, 10, 25, 0, 55, 0, 0, time.UTC)))
	if !strings.Contains(r, "с 02:55 25.10 UTC+2 до 02:05 25.10 UTC+1") {
		t.Errorf("resolved across the autumn change:\n%s", r)
	}
}

func TestNotificationTimesHalfHourZone(t *testing.T) {
	m := buildMessages("en", "", newZoneClock("Asia/Kolkata", testNow))
	if out := shown(t, m.TelegramHTML, sampleData(1, "firing")); !strings.Contains(out, "since 19:33 15.11 UTC+5:30") {
		t.Errorf("Asia/Kolkata:\n%s", out)
	}
	m = buildMessages("en", "", newZoneClock("America/St_Johns", testNow))
	if out := shown(t, m.TelegramHTML, sampleData(1, "firing")); !strings.Contains(out, "since 10:33 15.11 UTC-3:30") {
		t.Errorf("America/St_Johns:\n%s", out)
	}
}

func TestAlertmanagerConfigUsesTimeZone(t *testing.T) {
	doc := `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  services:
    - {name: api, type: http, critical: true}
  notifications:
    timeZone: Asia/Almaty
    receivers:
      - name: tg
        telegram: {chatID: -1, botTokenSecret: {name: s, key: k}}
      - name: mail
        email: {to: [a@example.com], from: k@example.com, smarthost: "smtp.example.com:587"}
    routing: {critical: [tg, mail]}
`
	b := generateYAML(t, doc)
	recv := b.AlertmanagerConfig["receivers"].([]any)
	var tg, mail map[string]any
	for _, r := range recv {
		r := r.(map[string]any)
		if c, ok := r["telegramConfigs"]; ok {
			tg = c.([]any)[0].(map[string]any)
		}
		if c, ok := r["emailConfigs"]; ok {
			mail = c.([]any)[0].(map[string]any)
		}
	}
	for name, text := range map[string]string{"telegram": tg["message"].(string), "email html": mail["html"].(string), "email text": mail["text"].(string)} {
		if !strings.Contains(text, `.UTC.Add 18000000000000).Format "15:04 02.01"`) || !strings.Contains(text, "UTC+5") || strings.Contains(text, " tz ") {
			t.Errorf("%s template does not use the fixed UTC+5 offset:\n%s", name, text)
		}
	}
}

func TestTimeZoneValidation(t *testing.T) {
	base := `apiVersion: kamerton.dev/v1alpha1
kind: SystemDescription
metadata: {name: shop}
spec:
  services:
    - {name: api, type: http}
  notifications:
    timeZone: %s
`
	for zone, ok := range map[string]bool{
		"Asia/Almaty": true, "Europe/Berlin": true, "UTC": true, "Etc/GMT-5": true,
		"Asia/Calcutta": true, // an old name; Kamerton resolves it itself, Alertmanager never sees the name
		"Asia/Almati":   false, "Local": false, "UTC+5": false, "'../etc/passwd'": false, `"Asia/Almaty "`: false,
	} {
		res := description.Validate([]byte(strings.Replace(base, "%s", zone, 1)))
		if res.Valid != ok {
			t.Errorf("%s: valid=%v, errors %+v", zone, res.Valid, res.Errors)
			continue
		}
		if !ok {
			if len(res.Errors) == 0 || res.Errors[0].Path != "/spec/notifications/timeZone" {
				t.Errorf("%s: error must point at the field: %+v", zone, res.Errors)
			}
		}
	}
}
