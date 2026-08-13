package commerce

import "time"

type Order struct {
	ID                  string
	UserID              string
	SKU                 string
	Status              string
	PaidAmountCents     int64
	RefundedAmountCents int64
	DeliveredAt         *time.Time
}

type OrderContext struct {
	Order           Order
	ProductCategory string
}

type Shipment struct {
	ID            string
	OrderID       string
	Status        string
	UntrustedNote string
	UpdatedAt     time.Time
}

type Inventory struct {
	SKU       string
	Available int
	Reserved  int
}

type Eligibility struct {
	Eligible   bool
	ReasonCode string
	Deadline   *time.Time
}

type WriteResult struct {
	ResourceType string
	ResourceID   string
	Status       string
	Replayed     bool
}
