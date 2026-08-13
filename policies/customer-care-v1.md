---
policy_id: customer_care
version: v1
effective_from: 2026-01-01T00:00:00Z
effective_to: 2027-01-01T00:00:00Z
region: CN
product_category: electronics
risk_level: high_risk_write
max_coupon_cents: 2000
---
# Customer care

## Coupon compensation

Coupon compensation for an eligible service recovery must not exceed 2000 cents. Issuing a coupon requires the customer-care compensation policy to be effective for the order region and product category.

## Approval boundary

A coupon is a high-risk write. Planning may describe the proposed amount, but execution requires explicit runtime approval and the coupon amount must be no greater than the typed `max_coupon_cents` limit.
