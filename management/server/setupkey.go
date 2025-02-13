package server

import (
	"context"
	"crypto/sha256"
	b64 "encoding/base64"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"github.com/netbirdio/netbird/management/server/activity"
	"github.com/netbirdio/netbird/management/server/status"
)

const (
	// SetupKeyReusable is a multi-use key (can be used for multiple machines)
	SetupKeyReusable SetupKeyType = "reusable"
	// SetupKeyOneOff is a single use key (can be used only once)
	SetupKeyOneOff SetupKeyType = "one-off"

	// DefaultSetupKeyDuration = 1 month
	DefaultSetupKeyDuration = 24 * 30 * time.Hour
	// DefaultSetupKeyName is a default name of the default setup key
	DefaultSetupKeyName = "Default key"
	// SetupKeyUnlimitedUsage indicates an unlimited usage of a setup key
	SetupKeyUnlimitedUsage = 0
)

const (
	// UpdateSetupKeyName indicates a setup key name update operation
	UpdateSetupKeyName SetupKeyUpdateOperationType = iota
	// UpdateSetupKeyRevoked indicates a setup key revoked filed update operation
	UpdateSetupKeyRevoked
	// UpdateSetupKeyAutoGroups indicates a setup key auto-assign groups update operation
	UpdateSetupKeyAutoGroups
	// UpdateSetupKeyExpiresAt indicates a setup key expiration time update operation
	UpdateSetupKeyExpiresAt
)

// SetupKeyUpdateOperationType operation type
type SetupKeyUpdateOperationType int

func (t SetupKeyUpdateOperationType) String() string {
	switch t {
	case UpdateSetupKeyName:
		return "UpdateSetupKeyName"
	case UpdateSetupKeyRevoked:
		return "UpdateSetupKeyRevoked"
	case UpdateSetupKeyAutoGroups:
		return "UpdateSetupKeyAutoGroups"
	case UpdateSetupKeyExpiresAt:
		return "UpdateSetupKeyExpiresAt"
	default:
		return "InvalidOperation"
	}
}

// SetupKeyUpdateOperation operation object with type and values to be applied
type SetupKeyUpdateOperation struct {
	Type   SetupKeyUpdateOperationType
	Values []string
}

// SetupKeyType is the type of setup key
type SetupKeyType string

// SetupKey represents a pre-authorized key used to register machines (peers)
type SetupKey struct {
	Id string
	// AccountID is a reference to Account that this object belongs
	AccountID string `json:"-" gorm:"index"`
	Key       string
	KeySecret string
	Name      string
	Type      SetupKeyType
	CreatedAt time.Time
	ExpiresAt time.Time
	UpdatedAt time.Time `gorm:"autoUpdateTime:false"`
	// Revoked indicates whether the key was revoked or not (we don't remove them for tracking purposes)
	Revoked bool
	// UsedTimes indicates how many times the key was used
	UsedTimes int
	// LastUsed last time the key was used for peer registration
	LastUsed time.Time
	// AutoGroups is a list of Group IDs that are auto assigned to a Peer when it uses this key to register
	AutoGroups []string `gorm:"serializer:json"`
	// UsageLimit indicates the number of times this key can be used to enroll a machine.
	// The value of 0 indicates the unlimited usage.
	UsageLimit int
	// Ephemeral indicate if the peers will be ephemeral or not
	Ephemeral bool
}

// Copy copies SetupKey to a new object
func (key *SetupKey) Copy() *SetupKey {
	autoGroups := make([]string, len(key.AutoGroups))
	copy(autoGroups, key.AutoGroups)
	if key.UpdatedAt.IsZero() {
		key.UpdatedAt = key.CreatedAt
	}
	return &SetupKey{
		Id:         key.Id,
		AccountID:  key.AccountID,
		Key:        key.Key,
		KeySecret:  key.KeySecret,
		Name:       key.Name,
		Type:       key.Type,
		CreatedAt:  key.CreatedAt,
		ExpiresAt:  key.ExpiresAt,
		UpdatedAt:  key.UpdatedAt,
		Revoked:    key.Revoked,
		UsedTimes:  key.UsedTimes,
		LastUsed:   key.LastUsed,
		AutoGroups: autoGroups,
		UsageLimit: key.UsageLimit,
		Ephemeral:  key.Ephemeral,
	}
}

// EventMeta returns activity event meta related to the setup key
func (key *SetupKey) EventMeta() map[string]any {
	return map[string]any{"name": key.Name, "type": key.Type, "key": key.KeySecret}
}

// hiddenKey returns the Key value hidden with "*" and a 5 character prefix.
// E.g., "831F6*******************************"
func hiddenKey(key string, length int) string {
	prefix := key[0:5]
	if length > utf8.RuneCountInString(key) {
		length = utf8.RuneCountInString(key) - len(prefix)
	}
	return prefix + strings.Repeat("*", length)
}

// IncrementUsage makes a copy of a key, increments the UsedTimes by 1 and sets LastUsed to now
func (key *SetupKey) IncrementUsage() *SetupKey {
	c := key.Copy()
	c.UsedTimes++
	c.LastUsed = time.Now().UTC()
	return c
}

// IsValid is true if the key was not revoked, is not expired and used not more than it was supposed to
func (key *SetupKey) IsValid() bool {
	return !key.IsRevoked() && !key.IsExpired() && !key.IsOverUsed()
}

// IsRevoked if key was revoked
func (key *SetupKey) IsRevoked() bool {
	return key.Revoked
}

// IsExpired if key was expired
func (key *SetupKey) IsExpired() bool {
	if key.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(key.ExpiresAt)
}

// IsOverUsed if the key was used too many times. SetupKey.UsageLimit == 0 indicates the unlimited usage.
func (key *SetupKey) IsOverUsed() bool {
	limit := key.UsageLimit
	if key.Type == SetupKeyOneOff {
		limit = 1
	}
	return limit > 0 && key.UsedTimes >= limit
}

// GenerateSetupKey generates a new setup key
func GenerateSetupKey(name string, t SetupKeyType, validFor time.Duration, autoGroups []string,
	usageLimit int, ephemeral bool) (*SetupKey, string) {
	key := strings.ToUpper(uuid.New().String())
	limit := usageLimit
	if t == SetupKeyOneOff {
		limit = 1
	}

	expiresAt := time.Time{}
	if validFor != 0 {
		expiresAt = time.Now().UTC().Add(validFor)
	}

	hashedKey := sha256.Sum256([]byte(key))
	encodedHashedKey := b64.StdEncoding.EncodeToString(hashedKey[:])

	return &SetupKey{
		Id:         strconv.Itoa(int(Hash(key))),
		Key:        encodedHashedKey,
		KeySecret:  hiddenKey(key, 4),
		Name:       name,
		Type:       t,
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  expiresAt,
		UpdatedAt:  time.Now().UTC(),
		Revoked:    false,
		UsedTimes:  0,
		AutoGroups: autoGroups,
		UsageLimit: limit,
		Ephemeral:  ephemeral,
	}, key
}

// GenerateDefaultSetupKey generates a default reusable setup key with an unlimited usage and 30 days expiration
func GenerateDefaultSetupKey() (*SetupKey, string) {
	return GenerateSetupKey(DefaultSetupKeyName, SetupKeyReusable, DefaultSetupKeyDuration, []string{},
		SetupKeyUnlimitedUsage, false)
}

func Hash(s string) uint32 {
	h := fnv.New32a()
	_, err := h.Write([]byte(s))
	if err != nil {
		panic(err)
	}
	return h.Sum32()
}

// CreateSetupKey generates a new setup key with a given name, type, list of groups IDs to auto-assign to peers registered with this key,
// and adds it to the specified account. A list of autoGroups IDs can be empty.
func (am *DefaultAccountManager) CreateSetupKey(ctx context.Context, accountID string, keyName string, keyType SetupKeyType,
	expiresIn time.Duration, autoGroups []string, usageLimit int, userID string, ephemeral bool) (*SetupKey, error) {
	unlock := am.Store.AcquireWriteLockByUID(ctx, accountID)
	defer unlock()

	account, err := am.Store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}

	if err := validateSetupKeyAutoGroups(account, autoGroups); err != nil {
		return nil, err
	}

	setupKey, plainKey := GenerateSetupKey(keyName, keyType, expiresIn, autoGroups, usageLimit, ephemeral)
	account.SetupKeys[setupKey.Key] = setupKey
	err = am.Store.SaveAccount(ctx, account)
	if err != nil {
		return nil, status.Errorf(status.Internal, "failed adding account key")
	}

	am.StoreEvent(ctx, userID, setupKey.Id, accountID, activity.SetupKeyCreated, setupKey.EventMeta())

	for _, g := range setupKey.AutoGroups {
		group := account.GetGroup(g)
		if group != nil {
			am.StoreEvent(ctx, userID, setupKey.Id, accountID, activity.GroupAddedToSetupKey,
				map[string]any{"group": group.Name, "group_id": group.ID, "setupkey": setupKey.Name})
		} else {
			log.WithContext(ctx).Errorf("group %s not found while saving setup key activity event of account %s", g, account.Id)
		}
	}

	// for the creation return the plain key to the caller
	setupKey.Key = plainKey

	return setupKey, nil
}

// SaveSetupKey saves the provided SetupKey to the database overriding the existing one.
// Due to the unique nature of a SetupKey certain properties must not be overwritten
// (e.g. the key itself, creation date, ID, etc).
// These properties are overwritten: Name, AutoGroups, Revoked. The rest is copied from the existing key.
func (am *DefaultAccountManager) SaveSetupKey(ctx context.Context, accountID string, keyToSave *SetupKey, userID string) (*SetupKey, error) {
	unlock := am.Store.AcquireWriteLockByUID(ctx, accountID)
	defer unlock()

	if keyToSave == nil {
		return nil, status.Errorf(status.InvalidArgument, "provided setup key to update is nil")
	}

	account, err := am.Store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}

	var oldKey *SetupKey
	for _, key := range account.SetupKeys {
		if key.Id == keyToSave.Id {
			oldKey = key.Copy()
			break
		}
	}
	if oldKey == nil {
		return nil, status.Errorf(status.NotFound, "setup key not found")
	}

	if err := validateSetupKeyAutoGroups(account, keyToSave.AutoGroups); err != nil {
		return nil, err
	}

	// only auto groups, revoked status, and name can be updated for now
	newKey := oldKey.Copy()
	newKey.Name = keyToSave.Name
	newKey.AutoGroups = keyToSave.AutoGroups
	newKey.Revoked = keyToSave.Revoked
	newKey.UpdatedAt = time.Now().UTC()

	account.SetupKeys[newKey.Key] = newKey

	if err = am.Store.SaveAccount(ctx, account); err != nil {
		return nil, err
	}

	if !oldKey.Revoked && newKey.Revoked {
		am.StoreEvent(ctx, userID, newKey.Id, accountID, activity.SetupKeyRevoked, newKey.EventMeta())
	}

	defer func() {
		addedGroups := difference(newKey.AutoGroups, oldKey.AutoGroups)
		removedGroups := difference(oldKey.AutoGroups, newKey.AutoGroups)
		for _, g := range removedGroups {
			group := account.GetGroup(g)
			if group != nil {
				am.StoreEvent(ctx, userID, oldKey.Id, accountID, activity.GroupRemovedFromSetupKey,
					map[string]any{"group": group.Name, "group_id": group.ID, "setupkey": newKey.Name})
			} else {
				log.WithContext(ctx).Errorf("group %s not found while saving setup key activity event of account %s", g, account.Id)
			}

		}

		for _, g := range addedGroups {
			group := account.GetGroup(g)
			if group != nil {
				am.StoreEvent(ctx, userID, oldKey.Id, accountID, activity.GroupAddedToSetupKey,
					map[string]any{"group": group.Name, "group_id": group.ID, "setupkey": newKey.Name})
			} else {
				log.WithContext(ctx).Errorf("group %s not found while saving setup key activity event of account %s", g, account.Id)
			}
		}
	}()

	return newKey, nil
}

// ListSetupKeys returns a list of all setup keys of the account
func (am *DefaultAccountManager) ListSetupKeys(ctx context.Context, accountID, userID string) ([]*SetupKey, error) {
	user, err := am.Store.GetUserByUserID(ctx, LockingStrengthShare, userID)
	if err != nil {
		return nil, err
	}

	if !user.IsAdminOrServiceUser() || user.AccountID != accountID {
		return nil, status.NewUnauthorizedToViewSetupKeysError()
	}

	setupKeys, err := am.Store.GetAccountSetupKeys(ctx, LockingStrengthShare, accountID)
	if err != nil {
		return nil, err
	}

	return setupKeys, nil
}

// GetSetupKey looks up a SetupKey by KeyID, returns NotFound error if not found.
func (am *DefaultAccountManager) GetSetupKey(ctx context.Context, accountID, userID, keyID string) (*SetupKey, error) {
	user, err := am.Store.GetUserByUserID(ctx, LockingStrengthShare, userID)
	if err != nil {
		return nil, err
	}

	if !user.IsAdminOrServiceUser() || user.AccountID != accountID {
		return nil, status.NewUnauthorizedToViewSetupKeysError()
	}

	setupKey, err := am.Store.GetSetupKeyByID(ctx, LockingStrengthShare, keyID, accountID)
	if err != nil {
		return nil, err
	}

	// the UpdatedAt field was introduced later, so there might be that some keys have a Zero value (e.g, null in the store file)
	if setupKey.UpdatedAt.IsZero() {
		setupKey.UpdatedAt = setupKey.CreatedAt
	}

	return setupKey, nil
}

// DeleteSetupKey removes the setup key from the account
func (am *DefaultAccountManager) DeleteSetupKey(ctx context.Context, accountID, userID, keyID string) error {
	user, err := am.Store.GetUserByUserID(ctx, LockingStrengthShare, userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	if !user.IsAdminOrServiceUser() || user.AccountID != accountID {
		return status.NewUnauthorizedToViewSetupKeysError()
	}

	deletedSetupKey, err := am.Store.GetSetupKeyByID(ctx, LockingStrengthShare, keyID, accountID)
	if err != nil {
		return fmt.Errorf("failed to get setup key: %w", err)
	}

	err = am.Store.DeleteSetupKey(ctx, accountID, keyID)
	if err != nil {
		return fmt.Errorf("failed to delete setup key: %w", err)
	}

	am.StoreEvent(ctx, userID, keyID, accountID, activity.SetupKeyDeleted, deletedSetupKey.EventMeta())

	return nil
}

func validateSetupKeyAutoGroups(account *Account, autoGroups []string) error {
	for _, group := range autoGroups {
		g, ok := account.Groups[group]
		if !ok {
			return status.Errorf(status.NotFound, "group %s doesn't exist", group)
		}
		if g.Name == "All" {
			return status.Errorf(status.InvalidArgument, "can't add All group to the setup key")
		}
	}
	return nil
}
