#!/bin/bash
cd /home/salman/Projects/golang/clever-connect
go test ./internal/rclone/ > /tmp/rc_test_out.txt 2>&1
echo "exit=$?" >> /tmp/rc_test_out.txt
