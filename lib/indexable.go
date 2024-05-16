package lib

import "github.com/redis/go-redis/v9"

type IIndexable interface {
	Tag() string
	Save(*IndexContext, redis.Cmdable, *Txo)
	SetSpend(*IndexContext, redis.Cmdable, *Txo)
	AddLog(logName string, entry map[string]string)
	Logs() map[string]map[string]string
	IndexBySpent(idxName string, idxValue string)
	OutputIndex() map[string][]string
	IndexByScore(idxName string, idxValue string, score float64)
	ScoreIndex() map[string]map[string]float64
}

type Indexable struct {
	logs       map[string]map[string]string
	outIndex   map[string][]string
	scoreIndex map[string]map[string]float64
}

func (i *Indexable) Save(ic *IndexContext, rdb redis.Cmdable, txo *Txo) {}

func (i *Indexable) SetSpend(ic *IndexContext, rdb redis.Cmdable, txo *Txo) {}

func (i *Indexable) AddLog(logName string, logEntry map[string]string) {
	if i.logs == nil {
		i.logs = make(map[string]map[string]string)
	}
	log := i.logs[logName]
	if log == nil {
		log = make(map[string]string)
		i.logs[logName] = log
	}
	for k, v := range logEntry {
		log[k] = v
	}
}

func (i *Indexable) Logs() map[string]map[string]string {
	return i.logs
}

func (i *Indexable) IndexBySpent(idxName string, idxValue string) {
	if i.outIndex == nil {
		i.outIndex = make(map[string][]string)
	}
	i.outIndex[idxName] = append(i.outIndex[idxName], idxValue)
}

func (i *Indexable) OutputIndex() map[string][]string {
	return i.outIndex
}

func (i *Indexable) IndexByScore(idxName string, idxValue string, score float64) {
	if i.scoreIndex == nil {
		i.scoreIndex = make(map[string]map[string]float64, 1)
	}
	if i.scoreIndex[idxName] == nil {
		i.scoreIndex[idxName] = make(map[string]float64, 1)
	}
	i.scoreIndex[idxName][idxValue] = score
}

func (i *Indexable) ScoreIndex() map[string]map[string]float64 {
	return i.scoreIndex
}
