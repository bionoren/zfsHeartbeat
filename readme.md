Monitors the health of a ZFS system and notifies someone via pushover if something went wrong

Configure the variables at the top of the main function, compile, and run periodically (eg using cron)

Checks
------
Zpool status (is everything online)
SMART status (have x% of recent tests passed)

Reports
-------
Weekly status update (all is well, X free space in each pool)
Pushover notification if something goes wrong
