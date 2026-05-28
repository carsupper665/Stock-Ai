#!/usr/bin/env python3
"""
Download historical crypto data from Binance and format for backend import.
"""
import requests
import csv
from datetime import datetime, timedelta
import time

def download_binance_klines(symbol, interval='1m', days=7):
    """
    Download klines from Binance API.
    
    Args:
        symbol: Trading pair (e.g., 'BTCUSDT', 'ETHUSDT')
        interval: Kline interval (1m, 5m, 15m, 1h, 1d)
        days: Number of days of historical data
    
    Returns:
        List of klines
    """
    url = "https://api.binance.com/api/v3/klines"
    
    # Calculate time range
    end_time = datetime.utcnow()
    start_time = end_time - timedelta(days=days)
    
    start_ms = int(start_time.timestamp() * 1000)
    end_ms = int(end_time.timestamp() * 1000)
    
    params = {
        'symbol': symbol,
        'interval': interval,
        'startTime': start_ms,
        'endTime': end_ms,
        'limit': 1000  # Max per request
    }
    
    all_klines = []
    current_start = start_ms
    
    print(f"Downloading {symbol} {interval} data from {start_time} to {end_time}...")
    
    while current_start < end_ms:
        params['startTime'] = current_start
        
        try:
            response = requests.get(url, params=params, timeout=30)
            response.raise_for_status()
            klines = response.json()
            
            if not klines:
                break
            
            all_klines.extend(klines)
            
            # Update start time for next batch
            current_start = klines[-1][0] + 1  # Last kline open time + 1ms
            
            print(f"  Downloaded {len(all_klines)} klines so far...")
            
            # Rate limiting
            time.sleep(0.2)
            
        except requests.RequestException as e:
            print(f"  Error downloading data: {e}")
            break
    
    print(f"  Total downloaded: {len(all_klines)} klines")
    return all_klines

def convert_to_csv(klines, output_file):
    """
    Convert Binance klines to backend CSV format.
    
    Binance kline format:
    [
      [
        1499040000000,      // 0: Open time
        "0.01634000",       // 1: Open
        "0.80000000",       // 2: High
        "0.01575800",       // 3: Low
        "0.01577100",       // 4: Close
        "148976.11427815",  // 5: Volume
        1499644799999,      // 6: Close time
        "2434.19055334",    // 7: Quote asset volume
        308,                // 8: Number of trades
        "1756.87402397",    // 9: Taker buy base asset volume
        "28.46694368",      // 10: Taker buy quote asset volume
        "17928899.62484339" // 11: Ignore
      ]
    ]
    
    Backend CSV format:
    timestamp,open,high,low,close,volume
    2025-01-01T00:00:00Z,100,101,99,100,10
    """
    with open(output_file, 'w', newline='', encoding='utf-8') as f:
        writer = csv.writer(f)
        writer.writerow(['timestamp', 'open', 'high', 'low', 'close', 'volume'])
        
        for kline in klines:
            timestamp_ms = kline[0]
            timestamp = datetime.utcfromtimestamp(timestamp_ms / 1000).strftime('%Y-%m-%dT%H:%M:%SZ')
            open_price = float(kline[1])
            high_price = float(kline[2])
            low_price = float(kline[3])
            close_price = float(kline[4])
            volume = float(kline[5])
            
            writer.writerow([timestamp, open_price, high_price, low_price, close_price, volume])
    
    print(f"  Saved {len(klines)} klines to {output_file}")

def main():
    """Download BTC and ETH historical data."""
    # Configuration
    symbols = [
        ('BTCUSDT', 'BTC'),
        ('ETHUSDT', 'ETH')
    ]
    interval = '1m'
    days = 7  # Last 7 days
    
    for symbol, short_name in symbols:
        print(f"\n{'='*60}")
        print(f"Processing {short_name} ({symbol})")
        print(f"{'='*60}")
        
        # Download data
        klines = download_binance_klines(symbol, interval, days)
        
        if not klines:
            print(f"  No data downloaded for {symbol}")
            continue
        
        # Convert to CSV
        output_file = f"D:\\GO PROJ\\Stock-ai-new\\data\\{short_name.lower()}_1m_7d.csv"
        convert_to_csv(klines, output_file)
        
        # Print summary
        first_ts = datetime.utcfromtimestamp(klines[0][0] / 1000)
        last_ts = datetime.utcfromtimestamp(klines[-1][0] / 1000)
        print(f"  Data range: {first_ts} to {last_ts}")
        print(f"  First price: {klines[0][4]}, Last price: {klines[-1][4]}")
    
    print(f"\n{'='*60}")
    print("Download complete!")
    print(f"{'='*60}")

if __name__ == '__main__':
    main()
