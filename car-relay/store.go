package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type deviceRow struct {
	ID            uint   `gorm:"primaryKey"`
	DeviceID      string `gorm:"uniqueIndex"`
	PairCode      string `gorm:"index"`
	PairExpiresAt time.Time
}

func (deviceRow) TableName() string { return "car_devices" }

type Store struct {
	db *gorm.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&deviceRow{}); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) IssuePairCode(deviceID string, now time.Time) (string, error) {
	code, err := s.uniqueCode(now)
	if err != nil {
		return "", err
	}
	row := deviceRow{}
	err = s.db.Where("device_id = ?", deviceID).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		row = deviceRow{
			DeviceID:      deviceID,
			PairCode:      code,
			PairExpiresAt: now.Add(pairCodeTTL),
		}
		return code, s.db.Create(&row).Error
	}
	if err != nil {
		return "", err
	}
	row.PairCode = code
	row.PairExpiresAt = now.Add(pairCodeTTL)
	return code, s.db.Save(&row).Error
}

func (s *Store) SetPairCode(deviceID, code string, expires time.Time) error {
	row := deviceRow{}
	err := s.db.Where("device_id = ?", deviceID).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return s.db.Create(&deviceRow{
			DeviceID: deviceID, PairCode: code, PairExpiresAt: expires,
		}).Error
	}
	if err != nil {
		return err
	}
	row.PairCode = code
	row.PairExpiresAt = expires
	return s.db.Save(&row).Error
}

func (s *Store) DeviceByCode(code string, now time.Time) (string, bool, error) {
	row := deviceRow{}
	err := s.db.Where("pair_code = ? AND pair_expires_at > ?", code, now).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.DeviceID, true, nil
}

func (s *Store) uniqueCode(now time.Time) (string, error) {
	for i := 0; i < 8; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(1000000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		var count int64
		err = s.db.Model(&deviceRow{}).
			Where("pair_code = ? AND pair_expires_at > ?", code, now).
			Count(&count).Error
		if err != nil {
			return "", err
		}
		if count == 0 {
			return code, nil
		}
	}
	return "", fmt.Errorf("无法生成配对码")
}
