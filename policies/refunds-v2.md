---
policy_id: refunds
version: v2
effective_from: 2026-01-01T00:00:00Z
effective_to: 2027-01-01T00:00:00Z
region: CN
product_category: electronics
risk_level: high_risk_write
---
# Refunds

## Refundable balance

The refundable balance is the order's paid amount minus all amounts already refunded. A requested refund must be positive and must not exceed that remaining balance.

## Approval boundary

Issuing a refund is a high-risk write. Planning may calculate the proposed amount from current order facts, but execution requires explicit runtime approval and a transactional balance recheck.
