package chatwoot

import (
	"encoding/json"
	"os"
	"sync"
)

var (
	mappingPath    = "/app/dbdata/chatwoot_msg_mapping.json"
	msgMappings    = make(map[int]string)
	waToCwMappings = make(map[string]int)
	msgMetaMapping = make(map[int]mappingMeta)
	mapMutex       sync.RWMutex
)

type mappingMeta struct {
	WAID        string `json:"wa_id"`
	Participant string `json:"participant,omitempty"`
	Chat        string `json:"chat,omitempty"`
}

type mappingFile struct {
	CwToWa map[int]string      `json:"cw_to_wa"`
	WaToCw map[string]int      `json:"wa_to_cw"`
	CwMeta map[int]mappingMeta `json:"cw_meta,omitempty"`
}

func normalizeWAID(waID string) string {
	if len(waID) >= 5 && waID[:5] == "WAID:" {
		return waID[5:]
	}
	return waID
}

func LoadMappings() error {
	mapMutex.Lock()
	defer mapMutex.Unlock()

	file, err := os.ReadFile(mappingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var data mappingFile
	if err := json.Unmarshal(file, &data); err == nil && (data.CwToWa != nil || data.WaToCw != nil) {
		if data.CwToWa != nil {
			msgMappings = data.CwToWa
		}
		if data.WaToCw != nil {
			waToCwMappings = data.WaToCw
		}
		if data.CwMeta != nil {
			msgMetaMapping = data.CwMeta
		}
		for cwID, waID := range msgMappings {
			if _, ok := msgMetaMapping[cwID]; !ok {
				msgMetaMapping[cwID] = mappingMeta{WAID: normalizeWAID(waID)}
			}
		}
		return nil
	}

	// Backward compatibility with the old file format (map[int]string)
	old := make(map[int]string)
	if err := json.Unmarshal(file, &old); err != nil {
		return err
	}
	msgMappings = old
	for cwID, waID := range msgMappings {
		norm := normalizeWAID(waID)
		waToCwMappings[norm] = cwID
		msgMetaMapping[cwID] = mappingMeta{WAID: norm}
	}
	return nil
}

func SaveMappings() error {
	mapMutex.Lock()
	defer mapMutex.Unlock()

	data, err := json.Marshal(mappingFile{
		CwToWa: msgMappings,
		WaToCw: waToCwMappings,
		CwMeta: msgMetaMapping,
	})
	if err != nil {
		return err
	}

	return os.WriteFile(mappingPath, data, 0644)
}

func SetMapping(cwID int, waID string) {
	SetMappingMeta(cwID, waID, "", "")
}

func SetMappingMeta(cwID int, waID, participant, chat string) {
	waID = normalizeWAID(waID)
	mapMutex.Lock()
	msgMappings[cwID] = waID
	waToCwMappings[waID] = cwID
	msgMetaMapping[cwID] = mappingMeta{
		WAID:        waID,
		Participant: participant,
		Chat:        chat,
	}
	mapMutex.Unlock()
	SaveMappings()
}

func GetMapping(cwID int) (string, bool) {
	mapMutex.RLock()
	defer mapMutex.RUnlock()
	waID, ok := msgMappings[cwID]
	return waID, ok
}

func GetCWByWA(waID string) (int, bool) {
	waID = normalizeWAID(waID)
	mapMutex.RLock()
	defer mapMutex.RUnlock()
	cwID, ok := waToCwMappings[waID]
	return cwID, ok
}

func GetQuoteByCW(cwID int) (waID, participant, chat string, ok bool) {
	mapMutex.RLock()
	defer mapMutex.RUnlock()

	meta, exists := msgMetaMapping[cwID]
	if exists {
		return meta.WAID, meta.Participant, meta.Chat, meta.WAID != ""
	}

	var legacyWAID string
	legacyWAID, exists = msgMappings[cwID]
	if !exists || legacyWAID == "" {
		return "", "", "", false
	}
	return legacyWAID, "", "", true
}

func DeleteByWA(waID string) {
	waID = normalizeWAID(waID)
	mapMutex.Lock()
	cwID, ok := waToCwMappings[waID]
	if ok {
		delete(waToCwMappings, waID)
		delete(msgMappings, cwID)
		delete(msgMetaMapping, cwID)
	}
	mapMutex.Unlock()
	SaveMappings()
}
