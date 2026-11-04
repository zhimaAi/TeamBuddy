package localserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const skillDailyLimit = 5

type skillDailyQuota struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// 调用方持有 skillQuotaMu；计数属于本机，不随账号或云端服务切换。
func (s *Server) loadSkillQuota(now time.Time) error {
	if s.config.DataDir == "" {
		return errors.New("skill quota data directory is not configured")
	}
	if s.skillQuota == nil {
		quota := &skillDailyQuota{}
		data, err := os.ReadFile(filepath.Join(s.config.DataDir, "skill-daily-quota.json"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			if err := json.Unmarshal(data, quota); err != nil {
				return err
			}
			if _, err := time.Parse("2006-01-02", quota.Date); err != nil || quota.Count < 0 || quota.Count > skillDailyLimit {
				return fmt.Errorf("invalid skill daily quota")
			}
		}
		s.skillQuota = quota
	}
	date := now.Format("2006-01-02")
	if s.skillQuota.Date != date {
		s.skillQuota = &skillDailyQuota{Date: date}
	}
	return nil
}

func (s *Server) saveSkillQuota() error {
	data, err := json.Marshal(s.skillQuota)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.config.DataDir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.config.DataDir, ".skill-daily-quota-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(s.config.DataDir, "skill-daily-quota.json"))
}
