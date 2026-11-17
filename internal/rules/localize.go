// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"fmt"

	"github.com/prometheus/prometheus/model/rulefmt"
)

// Annotations used by Kamerton's own notification templates. They hold the
// alert in plain words in the language of the description, separately from
// the English summary/description kept for Alertmanager, Grafana and other
// consumers:
//
//	kamerton_title      what is wrong, for example "Too many failed requests · operation X"
//	kamerton_value      the current value, as a Prometheus annotation template
//	kamerton_threshold  the threshold, already formatted
const (
	AnnotationTitle     = "kamerton_title"
	AnnotationValue     = "kamerton_value"
	AnnotationThreshold = "kamerton_threshold"
)

// Languages supported for notification text. English is the default; the
// default English alert titles are phrases[LanguageEnglish].titles below.
const (
	LanguageRussian = "ru"
	LanguageEnglish = "en"
)

type phraseSet struct {
	titles                                         map[string]string
	operation, pod, group, ms, cores, perHour, msg string
	perMinute, normally, ofNormal, noneAtAll       string
	container, restartsIn, anyOOM, ofLimit         string
	normalWord, moreThan                           string
	// noMembers and offsetStill take the duration (%s); or joins them.
	noMembers, offsetStill, or string
}

var phrases = map[string]phraseSet{
	LanguageRussian: {
		titles: map[string]string{
			AlertErrorRate:            "Много ошибочных запросов",
			AlertOperationErrorRate:   "Много ошибочных запросов",
			AlertLatency:              "Запросы выполняются слишком долго",
			AlertOperationLatency:     "Запросы выполняются слишком долго",
			AlertMemory:               "Под расходует слишком много памяти",
			AlertCPU:                  "Под расходует слишком много CPU",
			AlertRestarts:             "Контейнер перезапускается",
			AlertQueueLag:             "Очередь не успевают разбирать",
			AlertSLOBurnFast:          "Бюджет ошибок SLO быстро сгорает",
			AlertSLOBurnSlow:          "Бюджет ошибок SLO сгорает",
			AlertRequestsStopped:      "Запросы перестали поступать",
			AlertOOMKilled:            "Контейнер убит из-за нехватки памяти",
			AlertMemoryNearLimit:      "Память пода близка к лимиту",
			AlertCPUNearLimit:         "Процессор пода близок к лимиту",
			AlertCPUAboveNormal:       "Процессор загружен намного больше обычного",
			AlertLatencyAboveNormal:   "Ответы намного медленнее обычного",
			AlertQueueConsumerStopped: "Очередь перестали читать",
		},
		operation: "операция", pod: "под", group: "группа", ms: "мс", cores: "ядра", perHour: "в час", msg: "сообщений",
		perMinute: "в мин", normally: "обычно", ofNormal: "от обычного", noneAtAll: "ни одного запроса",
		container: "контейнер", restartsIn: "перезапуск(а) за", anyOOM: "любое убийство по памяти (OOMKilled)", ofLimit: "лимита",
		normalWord: "от обычного", moreThan: "и больше",
		noMembers: "в группе нет потребителей %s", offsetStill: "смещение группы стоит %s при непрочитанных сообщениях", or: "или",
	},
	LanguageEnglish: {
		titles: map[string]string{
			AlertErrorRate:            "Too many failed requests",
			AlertOperationErrorRate:   "Too many failed requests",
			AlertLatency:              "Requests are too slow",
			AlertOperationLatency:     "Requests are too slow",
			AlertMemory:               "Pod uses too much memory",
			AlertCPU:                  "Pod uses too much CPU",
			AlertRestarts:             "Container is restarting",
			AlertQueueLag:             "Queue consumers fall behind",
			AlertSLOBurnFast:          "SLO error budget burns fast",
			AlertSLOBurnSlow:          "SLO error budget is burning",
			AlertRequestsStopped:      "Requests stopped arriving",
			AlertOOMKilled:            "Container killed for running out of memory",
			AlertMemoryNearLimit:      "Pod memory close to the limit",
			AlertCPUNearLimit:         "Pod CPU close to the limit",
			AlertCPUAboveNormal:       "CPU far above normal",
			AlertLatencyAboveNormal:   "Requests far slower than normal",
			AlertQueueConsumerStopped: "Nobody reads the queue",
		},
		operation: "operation", pod: "pod", group: "group", ms: "ms", cores: "cores", perHour: "per hour", msg: "messages",
		perMinute: "per min", normally: "normally", ofNormal: "of normal", noneAtAll: "no calls at all",
		container: "container", restartsIn: "restart(s) in", anyOOM: "any out-of-memory kill (OOMKilled)", ofLimit: "of the limit",
		normalWord: "normal", moreThan: "and more than",
		noMembers: "no consumers in the group for %s", offsetStill: "the group offset stands still for %s with unread messages", or: "or",
	},
}

func (m Meta) phrases() phraseSet {
	if p, ok := phrases[m.Language]; ok {
		return p
	}
	return phrases[LanguageEnglish]
}

// explain adds the plain-words annotations. detail, when not empty, is
// appended to the title (for example the operation or pod).
func (m Meta) explain(r *rulefmt.Rule, detail, value, threshold string) {
	title := m.phrases().titles[r.Alert]
	if title == "" {
		title = r.Alert
	}
	if detail != "" {
		title = fmt.Sprintf("%s · %s", title, detail)
	}
	if r.Annotations == nil {
		r.Annotations = map[string]string{}
	}
	r.Annotations[AnnotationTitle] = title
	if value != "" {
		r.Annotations[AnnotationValue] = value
	}
	if threshold != "" {
		r.Annotations[AnnotationThreshold] = threshold
	}
}
