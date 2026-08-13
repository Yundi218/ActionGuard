---
policy_id: damaged_goods
version: v3
effective_from: 2026-01-01T00:00:00Z
effective_to: 2027-01-01T00:00:00Z
region: CN
product_category: electronics
risk_level: write
---
# Damaged goods

## Replacement eligibility

A delivered electronics order is eligible for replacement when the customer reports that the item arrived damaged and the request is made within 30 days after delivery. The order must belong to the requesting customer.

## Replacement window

The 30-day window begins at the recorded delivery time. A request at or before the end of day 30 remains eligible; a request after that window is not eligible under this policy version.

## Inventory requirement

The requested target SKU must exist, and the requested target SKU must have available inventory. Creating the replacement reserves one unit of that target SKU atomically.
