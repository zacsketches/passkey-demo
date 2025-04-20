# some simple line stuff to cut & paste for testing

# registering alice
http://localhost:8080/register.html?user=alice&invite=secret-token

# Looking at the users table
sqlite3 -header -csv ./db/users.db "SELECT * FROM users;" | column -t -s,

#Looking at the logins table
sqlite3 -header -csv ./db/users.db "SELECT * FROM logins ORDER BY ts DESC;" | column -t -s,

