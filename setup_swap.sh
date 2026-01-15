#!/bin/bash

# Size of swap file in Gigabytes
SWAP_SIZE=2G

echo "Check for existing swap..."
if grep -q "swapfile" /etc/fstab; then
    echo "Swap file already exists in /etc/fstab. Exiting."
    exit 1
fi

echo "Creating $SWAP_SIZE swap file..."
sudo fallocate -l $SWAP_SIZE /swapfile

echo "Setting permissions..."
sudo chmod 600 /swapfile

echo "Setting up swap space..."
sudo mkswap /swapfile

echo "Enabling swap..."
sudo swapon /swapfile

echo "Making swap persistent..."
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab

echo "Swap setup complete!"
echo "Current Swap Status:"
sudo swapon --show
free -h
