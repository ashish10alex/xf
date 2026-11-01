// Helpers shared by the definitions.

function dateFilter(col) {
  return `${col} >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)`;
}

module.exports = { dateFilter };
