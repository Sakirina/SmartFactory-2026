import React from 'react';
import { createRoot } from 'react-dom/client';
import { ConfigProvider, App as AntApp } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import { Application } from './shell';
import './style.css';

createRoot(document.getElementById('root')!).render(<React.StrictMode><ConfigProvider button={{ autoInsertSpace: false }} locale={zhCN} theme={{ token: { colorPrimary: '#187d72', borderRadius: 7, colorBgLayout: '#f3f5f7', colorText: '#26364a', colorBorder: '#dbe2e8', fontFamily: '-apple-system, BlinkMacSystemFont, "PingFang SC", "Microsoft YaHei", sans-serif' } }}><AntApp><Application /></AntApp></ConfigProvider></React.StrictMode>);
