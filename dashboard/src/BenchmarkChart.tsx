import React from 'react';
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
  ResponsiveContainer,
  Label,
} from 'recharts';

/**
 * Type definition for a single benchmark data point.
 */
type BenchmarkData = {
  version: string;
  tps: number;
  date: string;
};

/**
 * Publicly available benchmark data for Canton Network versions.
 * In a real-world scenario, this might be fetched from a static JSON file
 * or an API endpoint to allow for easier updates. For a self-contained
 * dashboard, embedding it is a viable approach.
 *
 * Workload: `simple_transfer` on a standard 3-node network topology.
 */
const benchmarkData: BenchmarkData[] = [
  { version: '2.7', tps: 850, date: 'Jun 2023' },
  { version: '2.8', tps: 1100, date: 'Sep 2023' },
  { version: '3.0', tps: 1500, date: 'Dec 2023' },
  { version: '3.2', tps: 1850, date: 'Mar 2024' },
  { version: '3.4', tps: 2200, date: 'Jun 2024' },
  // Placeholder for future releases to illustrate ongoing performance enhancements.
  // { version: '3.6', tps: 2500, date: 'Sep 2024' },
];

/**
 * A custom tooltip component to provide a richer display of data on hover.
 */
const CustomTooltip = ({ active, payload, label }: any) => {
  if (active && payload && payload.length) {
    const data = payload[0].payload;
    return (
      <div style={{
        backgroundColor: 'rgba(255, 255, 255, 0.9)',
        border: '1px solid #ccc',
        padding: '12px',
        borderRadius: '8px',
        boxShadow: '0 4px 8px rgba(0,0,0,0.1)',
      }}>
        <p style={{ margin: 0, fontWeight: 'bold', color: '#333' }}>{`Canton v${data.version}`}</p>
        <p style={{ margin: '4px 0 0 0', color: '#8884d8' }}>{`Peak TPS: ${data.tps}`}</p>
        <p style={{ margin: '4px 0 0 0', color: '#666' }}>{`Release: ${data.date}`}</p>
      </div>
    );
  }
  return null;
};

/**
 * Renders a chart displaying the evolution of Canton Network's Transactions Per Second (TPS)
 * across different software versions. This component is designed to be a central part of
 * the public-facing performance dashboard.
 */
const BenchmarkChart: React.FC = () => {
  return (
    <div style={{
      width: '100%',
      height: 500,
      padding: '24px',
      boxSizing: 'border-box',
      backgroundColor: '#ffffff',
      borderRadius: '12px',
      boxShadow: '0 4px 12px rgba(0,0,0,0.05)',
      fontFamily: 'sans-serif',
    }}>
      <h2 style={{ textAlign: 'center', marginBottom: '10px', color: '#1a202c', fontWeight: 600 }}>
        Canton Network TPS Evolution
      </h2>
      <p style={{ textAlign: 'center', color: '#4a5568', marginTop: 0, marginBottom: '30px', fontSize: '1rem' }}>
        Performance benchmarks for the <code>simple_transfer</code> workload on a standard 3-node network.
      </p>
      <ResponsiveContainer>
        <LineChart
          data={benchmarkData}
          margin={{
            top: 5,
            right: 40,
            left: 30,
            bottom: 25,
          }}
        >
          <CartesianGrid strokeDasharray="3 3" stroke="#e2e8f0" />
          <XAxis dataKey="version" stroke="#4a5568" tick={{ fontSize: 12 }}>
             <Label value="Canton Version" offset={-20} position="insideBottom" fill="#4a5568" />
          </XAxis>
          <YAxis stroke="#4a5568" tick={{ fontSize: 12 }} domain={[0, 'dataMax + 500']}>
            <Label
              value="Transactions Per Second (TPS)"
              angle={-90}
              position="insideLeft"
              style={{ textAnchor: 'middle' }}
              fill="#4a5568"
            />
          </YAxis>
          <Tooltip content={<CustomTooltip />} cursor={{ stroke: '#cbd5e0', strokeDasharray: '3 3' }}/>
          <Legend
            verticalAlign="top"
            height={40}
            wrapperStyle={{ color: '#4a5568' }}
          />
          <Line
            type="monotone"
            dataKey="tps"
            stroke="#4f46e5"
            strokeWidth={3}
            activeDot={{ r: 8, stroke: '#c7d2fe', strokeWidth: 4 }}
            dot={{ r: 5 }}
            name="Peak TPS"
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
};

export default BenchmarkChart;