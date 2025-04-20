# some simple line stuff to cut & paste for testing

# Looking at the users table
sqlite3 -header -csv ./db/users.db "SELECT * FROM users;" | column -t -s,

#Looking at the logins table
sqlite3 -header -csv ./db/users.db "SELECT * FROM logins ORDER BY ts DESC;" | column -t -s,
