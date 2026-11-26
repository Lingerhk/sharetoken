package coordinator

import "database/sql"

// idleTickets counts acct's held tickets that have not started, in all and
// per node, whatever their age and whoever is connected: every one of them
// keeps a slot of its node (busyNodes) until it starts or is released.
func idleTickets(tx *sql.Tx, acct string) (total int, byNode map[string]int, err error) {
	rows, err := tx.Query(`SELECT node, COUNT(*) FROM jobs WHERE buyer_acct = ? AND status = 'held' AND started_at = 0 GROUP BY node`, acct)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	byNode = map[string]int{}
	for rows.Next() {
		var node string
		var n int
		if err := rows.Scan(&node, &n); err != nil {
			return 0, nil, err
		}
		byNode[node] = n
		total += n
	}
	return total, byNode, rows.Err()
}
