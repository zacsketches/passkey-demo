#!/bin/bash

# Set the path to your SQLite database
DB_PATH="./db/users.db"

# Prompt for the username to delete
echo "Enter the username to delete:"
read USERNAME

# Check if the username is empty
if [ -z "$USERNAME" ]; then
  echo "No username provided. Exiting..."
  exit 1
fi

# Check if the user exists in the database
USER_EXISTS=$(sqlite3 $DB_PATH "SELECT COUNT(*) FROM users WHERE name = '$USERNAME';")
if [ "$USER_EXISTS" -eq 0 ]; then
  echo "User '$USERNAME' not found in the database. Exiting..."
  exit 1
fi

# Delete credentials first (foreign key constraint)
echo "Deleting credentials for user: $USERNAME"
sqlite3 $DB_PATH "DELETE FROM credentials WHERE user_id = (SELECT id FROM users WHERE name = '$USERNAME');"

# Delete the user from the users table
echo "Deleting user: $USERNAME"
sqlite3 $DB_PATH "DELETE FROM users WHERE name = '$USERNAME';"

# Print success message
echo "Successfully deleted user $USERNAME and their credentials."
