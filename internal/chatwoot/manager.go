package chatwoot

import (
	"encoding/json"
	"os"
	"sync"
)

type InstanceConfig struct {
	Name            string `json:"name,omitempty"`
	URL             string `json:"url"`
	Token           string `json:"token"`
	AccountID       string `json:"account_id"`
	InboxID         string `json:"inbox_id"`
	EnableSignature bool   `json:"enable_signature"` // Novo campo!
}

var (
	configPath = "/app/dbdata/chatwoot_instances.json"
	instances  = make(map[string]InstanceConfig)
	mgrMutex   sync.RWMutex
)

func LoadConfigs() error {
	mgrMutex.Lock()
	defer mgrMutex.Unlock()

	file, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return json.Unmarshal(file, &instances)
}

func SaveConfigs() error {
	mgrMutex.Lock()
	defer mgrMutex.Unlock()

	data, err := json.MarshalIndent(instances, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

func GetOriginalConfig(name string) (InstanceConfig, bool) {
	mgrMutex.RLock()
	defer mgrMutex.RUnlock()
	cfg, ok := instances[name]
	return cfg, ok
}

func GetConfig(name string) (InstanceConfig, bool) {
	cfg, ok := GetOriginalConfig(name)
	if ok {
		return cfg, true
	}

	// Fallback to environment variables only if all required values exist
	envURL := os.Getenv("CHATWOOT_URL")
	envToken := os.Getenv("CHATWOOT_TOKEN")
	envAccountID := os.Getenv("CHATWOOT_ACCOUNT_ID")
	envInboxID := os.Getenv("CHATWOOT_INBOX_ID")
	if envURL != "" && envToken != "" && envAccountID != "" && envInboxID != "" {
		return InstanceConfig{
			URL:             envURL,
			Token:           envToken,
			AccountID:       envAccountID,
			InboxID:         envInboxID,
			EnableSignature: os.Getenv("CHATWOOT_SIGNATURE") == "true",
		}, true
	}

	return cfg, false
}

func SetConfig(name string, cfg InstanceConfig) error {
	mgrMutex.Lock()
	instances[name] = cfg
	mgrMutex.Unlock()
	return SaveConfigs()
}

func DeleteConfig(name string) error {
	mgrMutex.Lock()
	delete(instances, name)
	mgrMutex.Unlock()
	return SaveConfigs()
}

func GetAllConfigs() map[string]InstanceConfig {
	mgrMutex.RLock()
	defer mgrMutex.RUnlock()
	cp := make(map[string]InstanceConfig)
	for k, v := range instances {
		cp[k] = v
	}
	return cp
}
