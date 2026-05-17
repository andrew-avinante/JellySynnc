package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

type SyncedItem struct {
	ID             string    `db:"id"`
	RemoteID       string    `db:"remote_id"`
	JellyfinItemID string    `db:"jellyfin_item_id"`
	ProviderIDs    string    `db:"provider_ids"`
	Resolution     string    `db:"resolution"`
	Encoding       string    `db:"encoding"`
	StrmPath       string    `db:"strm_path"`
	CreatedAt      time.Time `db:"created_at"`
	UpdatedAt      time.Time `db:"updated_at"`
}

type SyncRun struct {
	ID           string     `db:"id"`
	StartedAt    time.Time  `db:"started_at"`
	CompletedAt  *time.Time `db:"completed_at"`
	Status       string     `db:"status"`
	ItemsAdded   int        `db:"items_added"`
	ItemsRemoved int        `db:"items_removed"`
	Error        *string    `db:"error"`
}

func Connect(dbPath string) (*sqlx.DB, error) {
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}

	db := sqlx.NewDb(sqlDB, "sqlite")

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pinging db: %w", err)
	}

	return db, nil
}

func InsertSyncedItem(db *sqlx.DB, item SyncedItem) error {
	_, err := db.NamedExec(`
		INSERT INTO synced_items (id, remote_id, jellyfin_item_id, provider_ids, resolution, encoding, strm_path)
		VALUES (:id, :remote_id, :jellyfin_item_id, :provider_ids, :resolution, :encoding, :strm_path)
	`, item)
	return err
}

func UpdateSyncedItem(db *sqlx.DB, item SyncedItem) error {
	_, err := db.Exec(`
		UPDATE synced_items
		SET remote_id = ?, strm_path = ?, encoding = ?, updated_at = datetime('now')
		WHERE id = ?
	`, item.RemoteID, item.StrmPath, item.Encoding, item.ID)
	return err
}

func DeleteSyncedItem(db *sqlx.DB, id string) error {
	_, err := db.Exec(`DELETE FROM synced_items WHERE id = ?`, id)
	return err
}

func GetAllSyncedItems(db *sqlx.DB) ([]SyncedItem, error) {
	var items []SyncedItem
	err := db.Select(&items, `SELECT * FROM synced_items`)
	return items, err
}

func InsertSyncRun(db *sqlx.DB, run SyncRun) error {
	_, err := db.Exec(`
		INSERT INTO sync_runs (id, started_at, status)
		VALUES (?, ?, ?)
	`, run.ID, run.StartedAt, run.Status)
	return err
}

func FinalizeSyncRun(db *sqlx.DB, id string, status string, added, removed int, errMsg *string) error {
	_, err := db.Exec(`
		UPDATE sync_runs
		SET completed_at = datetime('now'), status = ?, items_added = ?, items_removed = ?, error = ?
		WHERE id = ?
	`, status, added, removed, errMsg, id)
	return err
}
